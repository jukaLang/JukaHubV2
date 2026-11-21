# Widescreen (16:9)

Most 3D games of the Nintendo 64, PlayStation and Dreamcast were drawn for a
4:3 TV, so on the Smart Pro's 16:9 screen they sit between two black bars.
Several emulators have a **widescreen hack**: they draw what the game's
camera sees beyond the 4:3 edges, and the picture fills the screen. JukaHub
does it per console and per game, for one launch at a time.

It is a hack. Expect objects that pop in at the sides, 2D menus and videos
that stretch, and the odd game that draws nothing useful outside 4:3. That is
why it is **off unless you switch it on**.

## Turning it on

**Settings → Emulation → Adjust for** — pick the console, then switch
**Widescreen (16:9)** on. With *All consoles* picked the row only lists the
consoles that play wide.

| Console      | Emulator that draws it wide                         |
|--------------|-----------------------------------------------------|
| Nintendo 64  | Mupen64Plus-Next (GLideN64 renderer), ParaLLEl N64  |
| PlayStation  | SwanStation, Beetle PSX / Beetle PSX HW             |
| Dreamcast    | Flycast                                             |
| Mega Drive   | Genesis Plus GX Wide                                |

PCSX-ReARMed, the PlayStation emulator TrimUI ships, has no widescreen mode,
and plain Genesis Plus GX and PicoDrive draw 4:3 only. For those consoles pick
the emulator in the game's options (**Start** in the library → **Emulator**).
The Saturn's emulators have no widescreen hack.

**One game:** in the library press **Start** on the game → **Widescreen
(16:9)**: *Default* (the console's choice), *On* or *Off*. A game that
glitches can be turned off while the rest of its console stays wide, or one
showcase game turned on alone.

The rows are hidden on a 4:3 screen (the Brick). JukaHub looks at the shape
of the display, not at the model, so a TV that the firmware made the screen
counts as wide.

## What a launch changes

Only for that launch, and put back when RetroArch exits (like every other
emulation setting, see `player/racfgscope.go`):

- the emulator's own option, in the options file RetroArch reads for the game:
  `mupen64plus-aspect = "16:9 adjusted"`,
  `parallel-n64-aspectratiohint = "widescreen"`,
  `flycast_widescreen_hack` (and the older `reicast_widescreen_hack`) `= "enabled"`,
  `swanstation_GPU_WidescreenHack = "true"` with
  `swanstation_Display_AspectRatio = "16:9"`,
  `beetle_psx_hw_widescreen_hack = "enabled"` with
  `beetle_psx_hw_widescreen_hack_aspect_ratio = "16:9"` (Beetle PSX without
  `_hw`), `genesis_plus_gx_wide_h40_extra_columns = "10"`;
- RetroArch's picture shape: `aspect_ratio_index = "1"` (16:9) and
  `video_aspect_ratio_auto = "false"`.

The picture shape is set only when the emulator's option was: a 4:3 picture
stretched to 16:9 is worse than bars. A game you set to another aspect ratio
(**Aspect ratio** in the same options) keeps that ratio unless Widescreen is on
for it; with Widescreen on, 16:9 wins for that launch.

## When it does not work

The log (`jukahub.log`) says what was set, or why nothing was:

- *"has no 16:9 option JukaHub knows"* — the game runs in an emulator without
  a hack; pick one from the table.
- *"has no options file yet"* — the emulator has never been started from
  RetroArch on this card. Start any game of it once from RetroArch's own menu,
  quit, and launch again from JukaHub.
- *"uses the angrylion renderer; only GLideN64 draws 16:9"* — Mupen64Plus-Next
  only draws 16:9 with GLideN64; change *RDP plugin* in its core options.

## PlayStation analog controller

JukaHub plugs in a DualShock for the PlayStation games that read the sticks,
for each launch. Settings →
Controls → Game buttons for → PlayStation → **Analog controller**:

- **Auto** (the default) plugs in a DualShock for the games on JukaHub's
  analog list (Ape Escape, Spyro 2 and 3, Crash Team Racing, Gran Turismo,
  Metal Gear Solid, Tony Hawk's and about 65 more) and keeps the standard pad
  for the rest. On the Brick, which has no sticks, Auto never changes the pad.
- **Always** uses the DualShock for every game; **Never** keeps the standard
  pad.

A game can choose for itself in its options (**Analog controller**: Auto,
DualShock or Standard pad). JukaHub sets `input_libretro_device_p1` for that
launch only — 517 for PCSX-ReARMed and Beetle PSX, 261 for SwanStation — and
the left stick stays a stick. A remap of your own that sets the device still
wins. In PCSX-ReARMed a game that supports the sticks but does not switch them
on itself needs the emulator's analog button (L1 + R1 + Select). Light-gun
games are left alone.

Not yet checked on a TrimUI: see WIDE-HW-1 in `player/MODERNIZATION.md`.
