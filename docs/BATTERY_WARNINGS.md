# Battery warnings, Menu + volume brightness and the battery on the lights

## Battery warnings (Settings → Power)

| Row | Values | What it moves |
| --- | --- | --- |
| Low battery warning at | Auto, 5–30% | The red battery icon in the header, the stick lights' low-battery pulse (it clears 3% higher), the "Battery 5% — plug in now" message and the in-game line |
| Blink from | Auto, 3–15% | Where the header's battery icon blinks. Auto is half the warning level, at least 3% |
| Show over games | On (default) / Off | In a RetroArch game: "Battery 9%" on RetroArch's own message line, once for every percent the charge falls at or below the warning level, and the lights flash the level |
| Save and suspend at | Auto (3%), 4–10% | Where a running game is saved and the device suspends (under "Save and suspend on low battery") |

Auto keeps everything where it always was: the message at 5%, the lights at 10%
(clear at 13%), the icon red below 15% and blinking below 5%, save and suspend
at 3%. Nothing warns while the battery holds its charge, or when the level is
unknown, and a charging battery's icon stays green.

The in-game line needs RetroArch's network commands (on for every game the
library starts with Battery in games on, or `network_cmd_enable` in
retroarch.cfg). A percent that was said is not said again until the charge has
climbed back above the warning level. While Battery in games' lines run for the
game, they are the ones RetroArch shows down to their 5% line and only the
lights flash; below that the line speaks again. Nothing is ever drawn over a
game's picture.

## Menu + volume sets brightness (Settings → Display)

Hold Menu and press volume up or down to step the screen brightness, on Home and
in games.

- **Auto** (default): only where the firmware does not already do it: a
  JukaHub OS boot, or stock firmware with no `keymon` running.
- **On**: always. Where the firmware also does it, a press may move two steps.
- **Off**: the volume keys only change the volume.

If the system changed the volume for the same press, the volume is put back
to where it was a moment later.

## The battery on the lights (Settings → Stick lights)

- **L2 + R2 shows the battery** (on by default): on Home, press both
  triggers together and the lights show the charge for 2 seconds. Green from
  50% up, amber below, red at the low-battery level. Only part of the lights
  light up, matching the charge, where the hardware allows it. That is LED by
  LED, or zone by zone on the TrimUI driver (the Brick has four zones, the
  Smart Pro two). With a single zone the colour shows the charge. With the
  lights off, the flash turns them on for itself and hands them back
  afterwards. On Home, L2 and R2 alone wait a moment (180 ms) for the other
  trigger before doing their usual job. With anything open over Home (a menu,
  a panel, a video) there is no wait and no flash.
- **Charging shows a filling bar** (under Charging light): the charge in green
  quarters, breathing while it charges and steady when full, instead of the
  breath.

## Config keys

`battery_warn_pct`, `battery_blink_pct`, `battery_save_pct` (`auto` or a
percent), `battery_warn_games`, `menu_volume_brightness` (`auto`, `on`, `off`),
`led_battery_chord`, `led_ind_charging_bar`.
