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

## Related

- Ports and PortMaster installation: [PORTMASTER.md](PORTMASTER.md)
- Emulator discovery and the Emulators panel: [DEVELOPMENT.md](DEVELOPMENT.md)
- Config keys in general: [CONFIGURATION.md](CONFIGURATION.md)
