# Looks: shaders and overlays for RetroArch games

A **look** is a shader RetroArch draws over a game started from the Games
library: a CRT with scanlines, an LCD grid, or SkyWalker541's **Pixel
transparency** (PT), which shows a Game Boy screen's see-through pixels over
its backing. An **overlay** is a picture around the game (a border or a
bezel). JukaHub sets both for one launch only: RetroArch's own settings come
back when the game ends, and RetroArch started from the firmware's menu keeps
its own picture.

## Where to change it

**Settings → Emulation**, with **Adjust for** on *All consoles* or on one
console:

- **Look** — *Auto*, *RetroArch's own*, *Sharp*, *Smooth*, *Pixel perfect*,
  and each look whose files are on the card: *Scanlines*, *LCD grid*, *CRT*,
  *Pixel transparency*. For one console the first choice is *Default*, which
  follows the All consoles choice and says what that is. The All consoles
  choice is also **Settings → Games & emulators → Video filter**.
- **Tune look** / **Value** / **Reset look** — the settings of the look this
  console uses (below).
- **Crop overscan** — *On* cuts the edge of the picture a TV hid (NES, SNES
  and the other TV consoles), *Off* keeps the whole picture, and *RetroArch's
  own* (the default) leaves RetroArch's setting, which crops. RetroArch passes
  it to the emulator as a hint (`video_crop_overscan`); an emulator with its
  own overscan option (Snes9x, FCEUmm, Nestopia, Genesis Plus GX) follows
  that option instead, under Quick Menu → Core Options in RetroArch.
- **Overlay** — a border from RetroArch's overlay folder, per console too,
  or one of your own from the card's `Overlays` folder (below).
- **Download looks** — fetches the shaders and SkyWalker541's overlays
  (below).

A game can still choose its own look: **Start** on it in the library →
**Video filter**. The game's choice wins over its console's, and the
console's over All consoles.

## Auto

Auto is the default until you choose something else, and picks by console:

| Consoles | Look |
|---|---|
| Game Boy, Game Boy Color, Game Boy Advance | Pixel transparency |
| NES, SNES, Mega Drive, Master System, PC Engine, the Ataris, PlayStation, Sega CD, 32X, Neo Geo, arcade | CRT |
| Game Gear, Lynx, WonderSwan, Neo Geo Pocket | LCD grid |
| PSP, N64, DS, Dreamcast, Saturn, Jaguar | none — the handheld's GPU has no time left for a shader |

A look whose shader is not on the card leaves RetroArch's own picture for
that game; nothing is asked and nothing breaks. Auto never turns on one of
RetroArch's CPU filters (they would slow the game down); choose the look
yourself to use one. A choice you made before (*RetroArch's own* included)
is kept.

## Download looks

**Settings → Emulation → Download looks** needs Wi-Fi and fetches:

- the PT shader (`handheld/PT_SkyWalker541.glslp`), a light CRT
  (`crt/zfast-crt.glslp`) and a light LCD grid (`handheld/zfast-lcd.glslp`)
  from libretro's GLSL shader collection, with every file each preset names,
  into the `shaders_glsl` folder of RetroArch's shader directory. If GitHub
  does not answer for one of them, the file comes from RetroArch's own shader
  pack instead;
- SkyWalker541's TrimUI Brick overlays, into `overlays/SkyWalker541/` in
  RetroArch's overlay folder (only the `.cfg`, `.png`, `.txt` and `.md`
  files). They then appear in the **Overlay** list. They are drawn for the
  Brick's 1024×768 screen.

Run it again to update. A file you changed yourself after a download, or one
that was there before, is never replaced; the message at the end says how
many were kept. RetroArch set to a slang video driver (glcore, Vulkan) cannot
use these GLSL shaders and gets only the overlays.

Tip from SkyWalker541: his grid overlays and a grid shader together show the
grid twice, so pick one or the other for the Game Boys.

## Your own overlays

After the library scans the card, every console with games gets a folder
under `Overlays` at the top of the card, beside `Roms`: `Overlays/Game Boy/`,
`Overlays/PlayStation/`, … each with a `README.txt`. Put an overlay there:

- a **PNG the size of the screen** (1024×768 on the Brick and the Brick Pro,
  1280×720 on the Smart Pro and Smart Pro S), see-through where the game
  shows; or
- a **RetroArch overlay** (`.cfg`) with its pictures. A picture a `.cfg` in
  the same folder uses is not listed on its own.

What happens then:

- **Settings → Emulation → Adjust for** *that console* → **Overlay** lists
  them as *Your overlay · name*.
- Left at *Default*, the console plays the overlay in its own folder (the
  one named like the console; the first by name when there are several):
  dropping a picture in is enough. With the folder empty, *Default* follows
  All consoles as before. The console's own **None** or **RetroArch's own**
  leaves the folder unused.
- A picture needs a RetroArch overlay file to be shown, so JukaHub writes one
  for it in its data folder (`data/overlays/`: one full-screen layer, the
  picture's path, no buttons) and RetroArch loads that, fully opaque, for that
  launch only.

Each folder is made once: one you delete stays deleted. A read-only card gets
no folders. A folder under the console's short name (`Overlays/GBA`,
`Overlays/PS`, `Overlays/MD`: the names the `Roms` folders take) counts as
the console's too, and while one is there JukaHub makes no long-named folder
beside it. Its overlays are listed in **Overlay** but play only when you pick
one: that is how NextUI keeps its overlays, as choices for its in-game menu,
so a card shared with NextUI does not suddenly put them on every game.

The `Overlays` folder is shared by the FTP server and on the phone's **Files**
card, so pictures can be dropped in from a computer or a phone.

PICO-8 games take `Overlays/PICO-8` when they play in fake-08 (RetroArch).
The native PICO-8 (your own copy, when installed) draws the whole screen
itself: no overlay reaches it. Standalone emulators and NextUI paks do not use
RetroArch overlays either.

## Tune look

The three rows under **Look** change the settings the look's shader offers
(RetroArch's "shader parameters"): for a CRT the scanlines and mask, for PT
the Game system, the pixel effect (Off, Grid, LCD Dot), grid lines, backing
colour and more. **Tune look** picks a setting (**Left/Right**, or **A** for
the list with every value), **Value** changes it in the shader's own steps
(the shader's default is marked), and **Reset look** puts them all back.

Settings are kept per console and look: a console's own value wins over the
All consoles one. With Auto on All consoles, pick a console in **Adjust for**
to tune its look. Before any tuning, PT on the Game Boy and Game Boy Color
starts with the matching Game system.

## Is it still full speed?

After a game played with a look, its options sheet's **Shader speed** row
says whether it held full speed ([Games Library → Per-game launch
options](GAME_LIBRARY.md#per-game-launch-options)). PT's *LCD Dot* effect is
the costly one: if a Game Boy game drops below full speed, set **Pixel
effect** to *Grid* in Tune look, or choose a lighter look.

## Aspect ratio

**Settings → Emulation → Aspect ratio** sets the picture's shape for RetroArch
games: *Core provided* (what the emulator asks for), *4:3*, *16:9*, *3:2*,
*10:9*, *8:7*, *Square pixels* or *Full screen* (stretched), or *RetroArch's
own* (the default: nothing is written). Like the look it follows **Adjust
for**, so each console can have its own, and a game can choose its own in its
options sheet (Start in the library → **Aspect ratio**). It is written for
that launch only and put back when RetroArch exits.

RetroArch numbers its shapes, and the numbers moved between versions. JukaHub
uses the numbers of RetroArch 1.9 and newer (the ones Batocera writes) when retroarch.cfg shows a 1.9+ build saved it; for an older or
never-saved config only *4:3* and *16:9* are offered, the two numbers that
never moved. *10:9* and *Full screen* are written as a plain ratio (the
screen's own shape for Full screen), which every 1.9+ build reads. A core or
game override saved in RetroArch's own menu (`config/<core>/…cfg`) that sets
an aspect ratio still wins over this one.

*Pixel perfect* still scales by whole pixels at the chosen shape, so *Full
screen* may not fill the screen with it. **Hard GPU sync**, on the same page,
lowers input lag a little with RetroArch's gl video driver at some CPU cost;
V-sync itself is **Smooth motion**.

## Files

| File | What |
|---|---|
| `Apps/JukaHub/data/look-settings.json` | tuned values, `{"values": {"game-boy\|pt": {"NAME": 0.5}}}` (`all` for All consoles) |
| `Apps/JukaHub/data/looks-files.json` | checksums of the files Download looks wrote, so a re-run never replaces one you changed |
| `<RetroArch config>/global.glslp` | written for one launch (a marker line, `#reference` to the look's preset, then a `NAME = "value"` line per tuned setting) and removed when RetroArch exits; a `global.glslp` of your own is never touched (the look is skipped instead) |
| `Overlays/<console>/` | your own overlays (a PNG, or a RetroArch `.cfg` with its pictures) and a `README.txt` |
| `Apps/JukaHub/data/overlays/*.cfg` | the RetroArch overlay written for a picture from `Overlays` (rewritten when it changes) |
| `Apps/JukaHub/data/overlays-made.txt` | the `Overlays` folders JukaHub made, so one you delete is not made again |
| `Apps/JukaHub/data/game-ratio.json` | aspect ratio and hard GPU sync: `{"global": {"ratio": "4:3"}, "consoles": {"snes": {"ratio": "8:7", "hardSync": "on"}}, "games": {"<rom path>": {"ratio": "full"}}}` |

Settings keys: [Configuration](CONFIGURATION.md) (`videoFilter`,
`raTuningCrop`, `raTuning.<console>.look` / `.crop`).

## Credits

PT_SkyWalker541 and the TrimUI Brick overlays are by SkyWalker541 (the
overlays are CC0); zfast-crt and zfast-lcd are by SoltanGris42, from
libretro's glsl-shaders collection.
