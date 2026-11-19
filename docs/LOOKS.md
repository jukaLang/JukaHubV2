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
- **Overlay** — a border from RetroArch's overlay folder, per console too.
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

## Files

| File | What |
|---|---|
| `Apps/JukaHub/data/look-settings.json` | tuned values, `{"values": {"game-boy\|pt": {"NAME": 0.5}}}` (`all` for All consoles) |
| `Apps/JukaHub/data/looks-files.json` | checksums of the files Download looks wrote, so a re-run never replaces one you changed |
| `<RetroArch config>/global.glslp` | written for one launch (a marker line, `#reference` to the look's preset, then a `NAME = "value"` line per tuned setting) and removed when RetroArch exits; a `global.glslp` of your own is never touched (the look is skipped instead) |

Settings keys: [Configuration](CONFIGURATION.md) (`videoFilter`,
`raTuningCrop`, `raTuning.<console>.look` / `.crop`).

## Credits

PT_SkyWalker541 and the TrimUI Brick overlays are by SkyWalker541 (the
overlays are CC0); zfast-crt and zfast-lcd are by SoltanGris42, from
libretro's glsl-shaders collection.
