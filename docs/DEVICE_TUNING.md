# Device tuning: stick, power, ports

Four fixes for the quirks of running other people's software on an A133P
handheld. Each one is opt-out, scoped as narrowly as it can be, and reports what
it could not do instead of pretending.

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

| Key | Default | Meaning |
|-----|---------|---------|
| `powerProfiles` | `true` | the whole switch |
| `powerProfilePerf` | `performance` | governor name for the running game |
| `powerProfileBattery` | `powersave` → `schedutil` → `ondemand` | first one the CPU offers |

Honest limits: writing `scaling_governor` needs root on most firmwares. The app
checks whether the governor is writable before promising anything, and an
unsupported device gets one line — `Performance mode unavailable: governor is
read-only on this firmware` — instead of a switch that silently does nothing.
Nested launches are depth-counted, so quitting one emulator does not drop the
profile while another is still running.

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
