# Device tuning: stick, power, ports

Four fixes for the quirks of running other people's software on an A133P
handheld. Each one is opt-out, scoped as narrowly as it can be, and reports what
it could not do instead of pretending.

## The Brick-family canvas: type follows geometry

The Brick, Brick Pro and Hammer run the 1280×720 design on a **960×720** canvas
(same height, three quarters of the width) so SDL fills the 1024×768 panel
instead of letterboxing it. Scene geometry is scaled once at load — and so is
every logical font size (`uiScaleFontSize`: 0.75×, floor 9px). Text that kept its
1280 size on a 960 canvas overflows: buttons ellipsised to "Play …" and the
seven-group Media hint row ran off both edges.

Two rows adapt instead of clipping:

- The controller hint bar is measured before it is drawn (`planFooterHints`),
  then laid out with tighter gaps and, if needed, a smaller size. Hints are
  dropped from the end only when even the smallest type does not fit, so
  navigate / open / back always stay on screen.
- Home cards whose title no longer fits step down one type tier
  (card → helper → small) before the title is ellipsised, which is why the
  narrow Favorites and Browser cards keep the whole word.

Scenes are still authored at 1280×720, and labels with no width of their own are
ellipsised at the panel edge rather than drawn past it. Nothing in a config has
to change for the compact canvas. `JUKAHUB_BRICK_DUMP=1 go test -run
TestCompactCanvasRendersEveryScene` renders every shipped scene at 960×720 and
writes the frames to `tmp/` for a visual pass.

The canvas is normally chosen from the detected device. When the hardware cannot
be named, the panel's own aspect decides: a 4:3 display gets the compact canvas
instead of a letterboxed 16:9 strip, and the startup log says so (`[ui] panel
1024x768 (device not identified): 1024x768 panel, compact canvas 960x720`).

### How the device names itself

The panel only decides the canvas. Everything else about a Brick — the front
**F1 / F2** keys, the handheld texture and decode budgets, the LED and haptic
layout, the package `devices` gate — needs the *model*, and TrimUI images are not
consistent about exposing one. A Brick's kernel may describe only the SoC
(`sun50iw10`, `allwinner,sun50i-a100`), its hostname may be a build string, and
the model code may appear in neither. Identification therefore reads every
source the image offers and classifies the union:

1. **The launcher's environment.** `DEVICE` first (NextUI reports a Brick as
   `DEVICE=brick`), then `PLATFORM` (`tg3040`). The order is deliberate: NextUI
   reports the Brick as `PLATFORM=tg5040` as well, so the platform code alone
   would name a Smart Pro.
2. **A model code in the kernel's board description.** Device-tree `model` and
   `compatible` (`/proc/device-tree`, falling back to the sysfs mirror
   `/sys/firmware/devicetree/base/`), then the `Hardware` / `Revision` values in
   `/proc/cpuinfo`, then the hostname. A single unopposed code is the board; two
   conflicting codes are refused, and `TG3040` plus the name `Hammer` is a
   Hammer.
3. **The model names the image ships.** `/etc/os-release`,
   `/etc/trimui_version`, `/etc/trimui/version`, `/etc/version`, and the string
   table of `/usr/trimui/bin/MainUI` — the vendor launcher is the same source
   community launchers read with `strings MainUI | grep ^Trimui` to tell a Brick
   from a Smart Pro. The binary is scanned in chunks and only when a TrimUI file
   is present, so a desktop build never reads it.
4. **The panel.** When the image is demonstrably TrimUI (its own files exist) but
   nothing names a model, 1024×768 selects the Brick family and 1280×720 the
   Smart Pro family. The panel cannot name a variant, so the family's base model
   is used instead of guessing a Pro or a Hammer.

A model named by an earlier source wins over a later one; a name that contradicts
the panel loses to the panel's family. `JUKAHUB_DEVICE_MODEL` still overrides
everything, for a desktop build imitating a handheld. The result is cached for the
process: the board cannot change under a running app, and the frame-rate
controller reads it every frame.

The startup log records the evidence, so a device that stays unidentified says
what its firmware actually exposed:

```
[device] trimui-brick (from panel 1024x768): dt model="sun50iw10" compatible="allwinner,sun50i-a100" cpuinfo="Allwinner sun50iw10 (Family: sun50i-a100)" hostname="..." firmware="Trimui Brick" trimui=true panel=1024x768 env(device="" platform="" model="")
```

The same facts, minus the hostname, are on the `Device id` line of Diagnostics
and of an exported diagnostics bundle.

## PPSSPP stick calibration

`Start → Stick Calibration` (also offered on desktop when a `ppsspp.ini` is
found). Three seconds of holding a stick, then the fix is written.

The Smart Pro's left stick reaches PPSSPP through SDL, and two PPSSPP defaults
make it feel broken:

| PPSSPP key | Default | Effect on this device |
|------------|---------|----------------------|
| `AnalogLimiter` | `0.600` | clamps the stick to 60% of its range — a full push reads as half input |
| `AnalogDeadzone` | `0.150` | small pushes near centre vanish entirely |

The wizard measures the stick at rest (drift) and its reach while you roll it
around the rim, then writes both keys under `[ControlMapping]`:

- `AnalogLimiter = 0.000` disables the clamp. A stick that cannot reach half
  range is a limiter problem, not a deadzone problem, so this is always written.
- `AnalogDeadzone` sits just above the measured drift (floor `0.02`, ceiling
  `0.12`, never more than half the measured reach).

The first write copies the original to `ppsspp.ini.bak` and never overwrites
that backup again, so the pre-calibration state is always recoverable. Line
endings, comments and every other section are preserved. Restart PPSSPP for the
new values to load.

Where it looks for the ini: `<SD>/PSP/SYSTEM/ppsspp.ini`,
`<SD>/ppsspp/PSP/SYSTEM/`, `<SD>/Apps/PPSSPP/PSP/SYSTEM/`, then
`~/.config/ppsspp/PSP/SYSTEM/` and `~/.local/share/ppsspp/PSP/SYSTEM/`.

## Performance on launch, battery on return

Every emulator and PortMaster launch raises the CPU governor to `performance`
and drops it back when the child process exits, so the hub idles cheap while a
game runs at full speed.

On the Smart Pro the governor alone is not the whole story. The A133P is an
A100 bin that advertises a **2 GHz** top OPP, but some firmware boots with
`scaling_max_freq` at 1.8 GHz, and the performance governor can only reach what
that ceiling allows. The launch profile therefore also lifts the ceiling to the
highest frequency the policy advertises (2 GHz here) and writes the firmware's
own value back on return. That lift is the difference between "performance" and
the chip's full clock:

```
power profile: 2.0 GHz ceiling, performance for the running game
```

| Key | Default | Meaning |
|-----|---------|---------|
| `powerProfiles` | `true` | the whole switch |
| `powerProfilePerf` | `performance` | governor name for the running game |
| `powerProfileBattery` | `powersave` → `schedutil` → `ondemand` | first one the CPU offers |
| `powerProfileMaxFreq` | `auto` (top OPP) | clock ceiling for a launch; `auto`/`max`, `2000` (MHz), `1.8` (GHz) or `1800000` (kHz) |

The ceiling belongs to a launch, never to the hub, and the kernel clamps the
request to its nearest supported frequency — nothing invents an OPP it does not
have. A cap below the boot ceiling is written as well as a lift: on a firmware
that already runs at 2 GHz, `powerProfileMaxFreq: 1800` lowers the ceiling for
the game and puts 2 GHz back on exit. A single game can override the global cap
from the library's options sheet (**Start** / **O**), where its own clock wins
for that launch only; see
[docs/GAME_LIBRARY.md](GAME_LIBRARY.md#per-game-launch-options). All four cores share one policy on this chip, so a pinned
`performance` governor heats the whole SoC; the community guide linked below
measured the trade-off and recommends 1.8 GHz over 2 GHz for long sessions.

Honest limits: writing `scaling_governor` and `scaling_max_freq` needs root on
most firmwares. The app checks whether the governor is writable before promising
anything, and an unsupported device gets one line — `Performance mode
unavailable: governor is read-only on this firmware` — instead of a switch that
silently does nothing. If an emulator's own `launch.sh`/`cpufreq.sh` writes its
own governor or ceiling after the hub raised the profile, that script wins: the
child runs last and the hub does not fight it. Nested launches are depth-counted,
so quitting one emulator does not drop the profile while another is still
running.

Background on the TSP's shared policy, the stock `cpufreq.sh` defaults and the
2 GHz heat trade-off: <https://github.com/guiopen/tsp-optimization>.

### CPU boost in games and the heat guard

Two things keep a game's clock where it belongs: **CPU boost in games** raises
it, and the **heat guard** lowers it whenever the handheld gets too hot,
whoever set the clock.

**CPU boost in games** (Settings → Games & emulators, `cpuBoostGames`: `auto`
by default, `always` or `off`). The launch profile above lifts the governor and
the ceiling before the game starts, but the firmware's own launch scripts run
after it: TrimUI's `cpufreq.sh` files put an emulator back on `ondemand`,
sometimes under a lower ceiling, and Automatic CPU speed used to read a game
waiting on its GPU as quiet and step it down. A boosted launch is held instead.
Three seconds after the start, when the script's lines have run, every parked
core comes online and the clock is held at the top of what the launch may use:
`scaling_max_freq` there and, when the governor left is a dynamic one,
`scaling_min_freq` too, so no governor idles the clock between frames. With
Full speed while playing off, the top is the firmware's own ceiling. Auto
boosts N64, PSP, Dreamcast (NAOMI, Atomiswave), DS, Saturn, Jaguar, GameCube,
Wii and PS2 games, ports, and every game the Heavy-game boost takes, while the
PlayStation and the 8/16-bit consoles, which run at full speed well under the
top clock, keep following Automatic CPU speed (and save the battery); Always
boosts every game. A game's own options win: Performance there is a
boost, while Battery saver, Balanced or a CPU limit are not. An emulator on the
`userspace` governor (NextUI's minarch) sets its own clock and gets the heat
guard only. Everything goes back when the game ends, only where it is still
the value written, and the auto-performance marker in the temp directory
covers a crash. While it holds, the Automatic CPU speed row reads
`<game> · CPU boost · 2.0 GHz` (`heavy-game boost` for those games), and the
boost row shows the last session: `Last boosted: Tekken 3 · 2.0 GHz · peak 74 °C`.

**The heat guard** has no off switch, like the fan's emergency speed. Every two
seconds (in Automatic CPU speed's tick, whether that is on or not) it reads the
hottest CPU/GPU thermal zone and the battery's own `temp`, and moves one dial:
the highest clock the heat allows.

| Reading | What the dial does |
|---|---|
| 3 °C or more under the soft limit (battery: 2 °C) | after 20 s, one operating point back up every 15 s, never sooner than 45 s after a step down; a level that proved too hot is tried again only every 2 minutes, until the chip has been cool for 3 minutes |
| just under the soft limit | holds; Automatic CPU speed steps no higher |
| soft limit | one step under the clock in front at once, then one more every 20 s while the reading is not falling |
| hard limit | one step every 4 s |
| critical limit | straight to 1.0 GHz, then lower (down to 600 MHz) while it stays there |

Hot and very hot steps stop at the first operating point at or above 1.0 GHz:
the game slows, it does not stop. Automatic CPU speed and CPU boost keep their
clock under the dial. A game JukaHub does not clock (its own CPU setting, a
launch script's ceiling, Automatic CPU speed off, the Heavy-game boost with CPU
boost off) has its `scaling_max_freq` lowered to the dial, and a raised
`scaling_min_freq` with it, and both put back once the dial lifts, only where
the value is still the guard's: a newer value someone else wrote is theirs.

The limits are per model. Chip temperature in °C, soft / hard / critical, on
**Heat limit** → Standard:

| Model | Chip | Why |
|---|---|---|
| Smart Pro (TG5040) | 80 / 87 / 95 | the thresholds Automatic CPU speed always used |
| Smart Pro S (TG5050) | 82 / 88 / 95 | its fan is flat out by 75 °C |
| Brick Pro (TG4040) | 76 / 83 / 92 | a smaller body than the Smart Pro |
| Brick and Hammer (TG3040), an unidentified TrimUI | 75 / 82 / 92 | the smallest body, whose metal back is the heat sink |

The battery's are 45 / 50 / 55 °C on every model. **Cooler** takes 6, 5 and
3 °C off the chip's limits and 3 and 2 off the battery's soft and hard ones;
**Warmer** adds 4 to the chip's soft and hard limits and 2 to the battery's.
The critical limit always stays 10 °C under the kernel's own lowest hot or
critical trip point on the chip's zones, and each limit at least a few degrees
under the next. With no temperature reading at all the dial holds, and only
the kernel's own limits apply.

A RetroArch game shows one line on its message line when the guard first slows
it ("Hot (84 °C): CPU slowed to 1.6 GHz to cool down"), one at critical ("Very
hot (95 °C): CPU at 1.0 GHz, save and let it cool") and one when it is back at
full speed; JukaHub's in-game menu adds "(cooling)" to its temperature
meanwhile, and the hub gets a toast. The log's `heat` lines name the model's
limits and the kernel's trip points at start, every step (`84 °C (hot, limit
80 °C): CPU limit none → 1.8 GHz`) and each warm game's peak when it ends.
Settings → Games & emulators → **Heat limit** shows the temperature and what
the dial does now, and Drivers & hardware's Thermal line says where it starts.
`JUKAHUB_HEAT_GUARD=0` in the environment turns the guard off for one run, for
comparing.

### Heavy-game boost

An Xbox game (hakuX), a GameCube, Wii or PS2 game where the handheld plays
them, an Xbox stream (GreenOvercast, started from the X1 pill, Cloud Gaming or
the library's Ports row) and — on the A133P models (Smart Pro, Brick, Brick
Pro, Hammer) — a PSP, Dreamcast (NAOMI, Atomiswave), Saturn, Jaguar, N64 or DS
game get more than the launch profile: on 1 GB of RAM and four A53 cores none
of them has a frame to spare. The Smart Pro S plays the second group without
it. Settings → Games & emulators → **Heavy-game boost** (`xboxBoost`, on by
default; it was called Xbox boost until 2026-11-21 and the key kept that name)
does this for that launch only:

- the launch profile at full speed even with `powerProfiles` off (a
  `powerProfileMaxFreq` cap and the game's own CPU options still apply), and
  Automatic CPU speed stands aside, so the clock never steps down while the
  game waits on its GPU (CPU boost in games holds it at the top unless that is
  off, and only the [heat guard](#cpu-boost-in-games-and-the-heat-guard) slows
  it);
- every parked core online (`cpuN/online` reading `0`);
- the GPU's devfreq node (a name containing `gpu` under `/sys/class/devfreq`)
  on its `performance` governor, or with `min_freq` raised to `max_freq` where
  it has none;
- the memory controller's devfreq node, where the kernel has one (a name with
  `dram`, `ddr` or `dmc`), the same way: an emulator of a 3D console waits on
  memory as much as on the CPU;
- the read-ahead of the disk the game is read from
  (`/sys/block/<disk>/queue/read_ahead_kb`) raised to 1024 KB where it is
  lower — the kernel's default is 128 KB — so a disc image streamed during play
  is read in long runs;
- JukaHub's memory: its textures and byte caches are emptied before the start,
  then the Go heap is handed back, the warm speech recognizer, the media
  library scan and the video link prefetch stop, and the page cache is dropped
  and compacted (every game start already stops the offline AI and the Piper
  voice); a start in the first minutes after power-on sets up the swap
  (Extra memory) at once instead of waiting for its quiet moment;
- the game's process group at nice −5, ahead of background work, and JukaHub's
  own threads at nice 10 in the idle I/O class until the game ends (only
  threads that were higher are moved, and each gets its own value back);
- on a chip with a faster CPU cluster (the Smart Pro S: four cores at 2.0 GHz,
  four at 1.4 GHz) the game held to the fast cluster's cores from its start;
- "Hey Juka" closed for the game;
- streams only: Wi-Fi power saving off (`iw`, else `iwconfig`), as the
  Moonlight hand-off does.

Everything is put back when the game's process group is gone, and only where
the value is still the one written, so a launch script's own change wins. A
marker in the temp directory lets the next start put back what a crash left.
An Xbox game on the handheld is boosted differently, because JukaHub closes
for it (the screen hand-off, [CONTROLS.md](CONTROLS.md#hakux-on-the-smart-pro)):
the parked cores, GPU and memory clocks and the read-ahead are set just
before it quits and put back by that marker when it starts again after the
game, and the hand-off script gives the CPU the `performance` governor for
the game's length (JukaHub's memory, priority and wake word no longer
matter: it is not running).
The log's `xbox` lines name each change (`boost on for Halo: …`, `boost off
after 42m0s of Halo: 3 of 3 values put back`), and the Settings row shows what
the last session got.

What the boost cannot do is make an emulator itself faster: that is the
emulators' own settings (see [Emulator speed](#emulator-speed) for RetroArch's
cores and JukaHub's PPSSPP, and GameCube speed for Dolphin). In an Xbox game,
**Menu** opens hakuX's settings: a lower render scale (0.5× is the default)
and display frame skip matter most, and both are kept per game ("Apply
settings" and a restart for the scale). For a stream that stutters, pick 720p
and 30 fps in GreenOvercast's settings.

### Emulator speed

Settings → Emulation → **Speed** decides, per console ("Adjust for"), how much
accuracy the emulators trade for frames. Each launch from the library gets its
console's level for that launch only, put back when RetroArch exits:

| Level | What it writes |
|-------|----------------|
| **Fast** | Frames skipped only when the handheld falls behind (the cores' audio-buffer frameskip: `auto` in PCSX-ReARMed, FBNeo, MAME 2003 / 2003-Plus, PicoDrive, Genesis Plus GX, Snes9x, gpSP, mGBA, Handy, Beetle PCE Fast; PPSSPP's automatic frameskip; Flycast's automatic skip at "some"; YabaSanshiro's / Yabause's frameskip), Mupen64Plus-Next's threaded renderer and frame duping, melonDS's threaded renderer, DeSmuME's JIT, Virtual Jaguar's fast blitter, Opera's DSP thread; on every console above the 8/16-bit ones, RetroArch's run-ahead, preemptive frames and rewind off, an audio buffer of at least 64 ms for the frameskip to watch (unless that console's Latency is Low or Lowest) and, on the A133P with Threaded video on Auto, threaded video (software-drawn cores hand the GPU driver's work to another core) |
| **Fastest** | Fast, plus: PSP lazy texture caching, low curve detail, GPU read-backs skipped; Dreamcast DSP off, per-strip alpha sorting, no mipmaps or anisotropic filtering, more skippable frames; PCSX-ReARMed's GPU thread asynchronous; DeSmuME skipping one frame in two; ARMSX2's EE at 75% with mild cycle skipping; N64 Light graphics |
| **Emulators' own** | nothing |

**Auto** (the default) is by console and chip: the 8/16-bit consoles and the
handhelds keep their emulators' own settings; PlayStation, N64, DS, arcade and
the CD consoles get Fast; PSP, Dreamcast, Saturn, Jaguar and PS2 get Fastest on
the A133P models and Fast on the Smart Pro S. GameCube and Wii follow
**GameCube speed** (the Speed row with GameCube or Wii picked is that setting);
**PS2 speed** under Games & emulators is the PS2's own Speed.

A setting someone made wins: an option is written only where the core's
options file holds the core's own default for it, a key the file lacks is not
added, and a game's or a folder's own options file (RetroArch's "Save Game
Options") is left alone. A core that never ran has no options file yet;
RetroArch writes one when it exits, so its second start gets the level.
JukaHub's own PPSSPP gets the PSP's level in its `ppsspp.ini` the same way
(`FrameSkip 1` + `AutoFrameSkip`; Fastest adds `TextureBackoffCache`,
`SplineBezierQuality 0`, skipped GPU read-backs and no anisotropic filtering),
journalled in `emu-speed-ppsspp.json` and put back after the game. Heavy
consoles' cores also log warnings only once a core has played a game in this
run of JukaHub (a game that does not start is still diagnosed from the first
launch's full log). The log's `emulation` lines (`emulator speed for this
launch`) say what each launch got.

## PortMaster audio, per launch

Some ports start silent or crackly because they autodetect an SDL audio driver
the firmware does not have. JukaHub pins the driver **for the port process
only**:

| Key | Default | Meaning |
|-----|---------|---------|
| `portAudioWorkaround` | `true` (Linux only) | set `SDL_AUDIODRIVER` for port launches |
| `portAudioDriver` | `alsa` | driver name |
| `portAudioDevice` | *(empty)* | also sets `SDL_AUDIODEV` and `AUDIODEV` |

This is a child-process environment, not a system change: there is nothing to
restore if the port crashes or is killed, and JukaHub's own audio is untouched.
Non-Linux builds get no override at all.

## Ports that cannot run here

PortMaster ports declare their architectures in `port.json`
(`{"arch":["aarch64","armhf"]}`), with `info.txt` as a fallback. The Ports panel
reads that manifest and flags what this chip cannot run:

- `64-bit ready` — a 64-bit (or architecture-independent) build is present.
- `32-bit only` — the only builds are `armhf`/`i386`; the row says so, appears in
  the panel summary (`2 ports · 1 cannot run here (32-bit only)`), and pressing
  it explains instead of launching a binary that cannot load.

A small curated map in `port_compat.go` pins ports the community has
hand-checked; it overrides the manifest by folder name. Unknown ports get no
badge at all — a guess is worse than silence.

Manifests are sometimes wrong (a port with a 64-bit wrapper that does not list
`aarch64`). `portIgnoreCompat: true` in `jukauser.json` launches flagged ports
anyway; the badge stays visible so the decision is deliberate.

## Battery calibration

**Tools → Battery calibration** is for a battery % that jumps around or a
handheld that dies with charge still showing (the classic "dies at 15%"). It
runs the cycle ArkOS-style firmwares call calibration — one full charge, one
full drain — which is how the fuel gauge (the AXP PMIC on the Smart Pro and
Smart Pro S) relearns the cell's real capacity:

1. **Charge to full.** Plugged in until the battery says Full, or reads 100%
   with almost no charge current for ten minutes.
2. **Run it down.** Unplug and play as usual until the handheld switches
   itself off. A background sampler — not tied to any screen, so it keeps going
   behind games — logs the raw gauge and the voltage once a minute to
   `battery_cal_samples.jsonl` in the data folder (appended and fsynced, so a
   power cut keeps every earlier reading; thinned past 3000 readings).
3. **Finish.** On the next start JukaHub sees the last reading was 5% or less
   (or the voltage near the cutoff) and finishes by itself; any other ending
   gets one question on the screen: "Did it switch itself off?".

From the drain JukaHub also learns how far off the % was. The true charge at a
reading is the share of the run's use still ahead of it, so the method assumes
a fairly steady load and the screen says so. Use is counted from monotonic gaps
capped at five minutes; a longer gap (suspend, reboot, JukaHub not running)
counts for what the level dropped at the run's own pace, so a wrong wall clock
before NTP never stretches the run. The readings are binned at gauge 0, 5, …
100, pooled to be monotonic, and stored as a 21-point table in
`battery_cal.json`. The result is said in plain words ("Your battery read 14%
when it actually shut down", "The gauge is accurate within 3% — no correction
needed"), and **Correct the battery %** turns on by itself when the gauge was
more than 4 points off and the run was not rough. With it on, every reader of
the level — the corner, the battery saver, the low-battery warning and guard —
sees the corrected %, so the warning comes at a true 5% and the guard at a true
3%.

Honest limits: nothing is written to the PMIC (no register writes, no "reset
gauge" files — they differ between drivers and a wrong one can stop charging).
Charging, cancelling or Reset calibration never touch the battery itself. The
correction is learned on a discharge and applied while charging too, where the
gauge's curve differs a little. A run that was charged before it ran out goes
back to step 1. No battery reported (Windows desktops) shows "Not available on
this device".

## Stock Smart Pro hardware limits

On firmware 1.1.0, the AXP2202 battery exposes charge counters and a
read-only `constant_charge_current`, but no charging end threshold or writable
charge-stop control. JukaHub therefore does not offer an 80% charge limit on
this image. Changing the displayed battery percentage through calibration
does not stop charging.

The built-in ALSA codec identifies itself as `audiocodec`. JukaHub selects it
by that ID, so it can find the speaker controls even when a USB DAC takes card
0 at boot. `amixer -c audiocodec scontrols` worked on the tested Smart Pro;
volume on an attached USB DAC still needs a live test. Stock 1.1.0 keymon maps
its 20 audible steps to raw digital attenuation from 45 down to 0, with step 0
at raw 63; live steps 1 and 4 matched that map. JukaHub now uses the stock map
on this model. If the built-in codec is absent, it leaves volume alone rather
than changing an unrelated USB card.

The same firmware has configfs FunctionFS for USB file transfer, but its
kernel explicitly omits ECM and RNDIS gadget functions. It cannot offer
networking over USB without a different kernel.

## The FN switch

On the original Smart Pro the FN slide switch is a latching GPIO (gpio243,
the pin Travel lock reads). Settings → Power & battery → **FN switch does**
(`fnSwitchMode`) picks what sliding it does:

| Choice | Slid | Slid back |
|--------|------|-----------|
| Nothing (default) | — | — |
| Travel lock | the power button cannot wake or start the device | it works again |
| Mute | the speaker goes to 0 | the level it had comes back |
| Lights off | the stick lights go dark, in games too | the lights show your mode again |
| Airplane mode | Wi-Fi and Bluetooth off | exactly the radios that were on come back |

Travel lock is the old **Travel lock** switch, unchanged (its boot hook
included): a config with it on simply reads as this choice. Picking anything
else turns it off and takes the hook off the card.

Which position is which is not documented, so a pick stores the position the
switch is in right then as "off" (`fnSwitchAt`, like `travelLockAt`). A pin
that is not a switch never changes and so never does anything. The switch's
position decides, nothing is toggled blindly: slid on, only what is not muted,
dark or blocked yet changes; slid back, only what the switch did is undone.
What it holds is kept in `fnSwitchHeld`, so a restart with the switch slid back
unmutes and brings the radios back, and a restart with it slid on applies the
action again. A goroutine reads the pin every 250 ms (two equal readings make
a position), so the switch works while a game runs; toasts only show when
JukaHub has the screen. The row is hidden on every other device.

## Stick lights: critical battery, screen brightness, achievements

Three additions to Settings → Stick lights:

- **Critical battery.** With the Low battery warning on, at 5% or less and not
  charging the slow red breath becomes a faster (1 s), deeper and brighter
  pulse (the indicator brightness plus 40 points), until the charge is back at
  7%.
- **Follow screen brightness** (`led_follow_screen`, off): the lights'
  Brightness is scaled by the backlight's level, never below 10% and never
  above the Brightness you set; Off stays Off. The warnings keep their own
  Indicator brightness.
- **Celebrate achievements** (`led_celebrate`, on): a RetroAchievements unlock
  runs a three-second rainbow over the rings, over the game too, and then they
  show what they showed before. On rings where a colour change costs a dip it
  is six pure colours faded through dark. Nothing happens with the lights off,
  at Brightness Off, or where the LEDs have no colour.

## GPU boost in games

Settings → Power & battery → **GPU boost in games** (`gpuBoost`) does for
other games what Heavy-game boost does for the GPU: the GPU's devfreq node (a name
containing `gpu` under `/sys/class/devfreq`) runs its `performance` governor,
or has `min_freq` raised to `max_freq`, for the length of one launch.

| Value | Boosted launches |
|-------|------------------|
| `auto` (default) | N64, PSP, Dreamcast (and NAOMI / Atomiswave), Nintendo DS, Saturn, PlayStation, GameCube / Wii, PS2, and ports |
| `always` | every game and port |
| `off` | none |

It uses Heavy-game boost's own plan and lock, so the two never write the node
at once, and a launch Heavy-game boost already holds is left to it. Values are put back
when the game's process group is gone, only where they are still the ones
written; a marker in the temp directory (`jukahub-gpu-boost.json`) lets the
next start put back what a crash left. CPU governors are not touched
(Automatic CPU speed and the launch profile own them). The row only shows on a
handheld whose GPU has a devfreq node; the log's `gpu` lines say what each
launch got.

## Threaded video in RetroArch

RetroArch can run its video driver on a thread of its own (`video_threaded`).
That hides slow frames on a weak CPU, but frames reach the screen a little
later and less evenly, and on the Smart Pro S's A523 the uneven pacing shows.
**Settings → Emulation →
Threaded video**: *Auto* (the default) turns it off for RetroArch games started
from the library on a Smart Pro S and leaves RetroArch's own value on the other
TrimUIs and PCs; *On* and *Off* set it everywhere. Like the other Emulation
settings it is written for that launch and put back when RetroArch exits, so
games started from the firmware's menu keep RetroArch's own setting
(`raTuningThreaded` in [Configuration](CONFIGURATION.md)).

## Related

- Ports and PortMaster installation: [PORTMASTER.md](PORTMASTER.md)
- Emulator discovery and the Emulators panel: [DEVELOPMENT.md](DEVELOPMENT.md)
- Config keys in general: [CONFIGURATION.md](CONFIGURATION.md)
