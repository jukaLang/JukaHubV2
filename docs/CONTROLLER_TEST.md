# Controller test

Sticks drift. On a handheld that is the failure people actually hit: the menu
cursor creeps on its own, or a diagonal push reads as a different direction. The
Controller test shows the raw value every axis reports, whether the sticks come
back to centre when released, and which buttons register at all.

Open **Tools → Power tools → Controller test**.

## What it shows

- **Two stick boxes.** The dot is the live position. The ring is the deadzone the
  app actually applies to the left stick (`stickDeadzone`, 12000 of 32767), so
  "inside the ring" means the menu cursor will not move.
- **Numeric readout** under each stick, in the `-1.00 … +1.00` range.
- **L2 / R2 bars**, filled by travel. SDL reports triggers as `0 … 32767`.
- **Button chips.** A chip lights while the button is held, stays slightly
  brighter once it has been pressed, and shows its press count. A button that
  never lights is a button the pad never reports — that is what a dead shoulder
  or a mislabelled pad looks like.
- **A drift line** in the header, in the warning colour, naming the axes that
  need attention.

## How the drift verdict works

An axis is only judged after it has been unchanged for **900 ms**, so a stick you
are holding over is never called drifting. The resting value then decides:

| Resting magnitude | Verdict |
|-------------------|---------|
| below `0.05` | centred — no warning |
| `0.05` up to the app's deadzone | "rests at … — inside the deadzone": real drift, still too small to move the cursor |
| at or past the deadzone | "rests at … — past the deadzone": this is the one you feel |

Only the four stick axes are judged. A trigger resting at zero is normal.

If the left stick is flagged, the fix is the PPSSPP calibration screen
(**Emulators → Smart Pro Compatibility → stick calibration**) or a deadzone in the
emulator itself; the panel tells you which axis and how far off centre it is.

## Controls

| Input | Action |
|-------|--------|
| Click **Reset** | Clear every reading |
| Hold **SELECT** for 1.2 s | Close the panel |
| Keyboard R / Enter | Reset |
| Keyboard Esc | Close |
| Click outside the panel | Close |

The panel swallows every pad event on purpose — a press has to be observable, not
act on the UI — so the way out is a hold rather than a press. SELECT is the hold
because nothing else in JukaHub uses it inside a panel.

## What it does not measure

- **Input latency to the screen.** A button-to-photon number needs a camera
  pointed at the display. JukaHub draws menus, not games, and it cannot see an
  external emulator's frames. The panel shows press *durations* instead, which is
  what reveals a button that double-registers or sticks.
- **Whether a game reads the pad.** Only whether SDL receives the events.
- **Rumble.** Not every pad on these devices has it.

## Related

- [Device tuning](DEVICE_TUNING.md) for the PPSSPP stick calibration and power
  profiles.
- [Controls](CONTROLS.md) for the button map the rest of the app uses.
- [Power tools](CONTROLS.md#power-tools) for the panel it lives in.
