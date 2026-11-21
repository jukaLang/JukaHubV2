# Idle in games, idle hooks and the battery in games

A game left running on a table used to play on at full brightness until the
battery ran out: JukaHub's idle screen, its low-power state and Auto sleep all
stand down while a game owns the screen. Three settings cover the time you
are playing (or not):

- **Dim when idle in games** — the screen dims when nobody touches the
  controls, then the handheld sleeps or saves and powers off;
- **idle hooks** — your own scripts run when the handheld goes idle, idles
  for longer, and comes back;
- **Battery in games** — RetroArch games show the battery level and the
  time left on their own message line.

All three are new and **not yet checked on a TrimUI**.

## Dim when idle in games

Settings → Power & battery:

| Row | Stops | What it does |
| --- | --- | --- |
| **Dim when idle in games** | Off (default), 5, 10, 15, 30 min | No input for this long while a game runs: the backlight goes down to the lowest level JukaHub ever sets. |
| **Then sleep or power off after** | Off (default), 5, 10, 30, 60 min | Still no input this long after the dim: the action below. Off keeps the screen dim. |
| **When idle in games** | Sleep (default), Save and power off | What that action is. |

Any button, key, mouse move or stick push brings the screen back to exactly
the brightness it had. If you changed the brightness yourself while it was
dim (the firmware's brightness keys), your level stays.

- **Sleep** is the in-game menu's own Sleep: the game waits, paused, in
  memory, and a press of the power button wakes the handheld straight into
  it. If it stays asleep as long as **Power off after sleeping** says (6 h by
  default), the game is saved and the handheld powers off, as after any
  sleep; a low battery brings that time nearer.
- **Save and power off** is Power off's own path: a RetroArch game saves a
  state and quits, a standalone emulator saves on its way out where it can
  ([Never lose a session](SESSION_SAFETY.md)), and **Resume game at power
  on** brings the game back at the next power-on. While one of JukaHub's
  alarms is set it sleeps instead, because a handheld that is off cannot
  ring.

Neither happens while the battery is charging (nothing is at risk; the
screen just stays dim), while JukaHub's own music is playing, or while an
alarm rings. The game paused under the in-game menu or the game assistant
counts as in use.

What counts as input: every input device the kernel lists with buttons, keys,
sticks or a mouse — the built-in controls, a Bluetooth or USB pad, a keyboard
— read beside the game without taking anything away from it. A stick has to
move about an eighth of its travel, so a stick resting with a little jitter
does not keep the screen awake. A pad connected during the game is picked up
within ten seconds. Motion sensors are ignored.

A stream or a video watched without touching the controls dims too; push a
stick now and then, or leave the setting Off.

## Idle hooks

Put shell scripts in JukaHub's data folder (`Apps/JukaHub/data` on the
handheld):

```
Apps/JukaHub/data/hooks/idle/       the handheld went idle
Apps/JukaHub/data/hooks/extended/   it stayed idle: it is about to sleep or power off
Apps/JukaHub/data/hooks/active/     someone is using it again
```

Every file ending in `.sh` in the folder for a state runs with `/bin/sh` (it
does not need to be executable), in name order (`10-leds.sh` before
`20-wifi.sh`), one at a time. Each gets at most **10 seconds**; a script still
running then is stopped together with everything it started. Hidden files,
links and other names are skipped, and at most 32 scripts run per folder.
What each script did, and the first line it printed, is in JukaHub's log.

Each script gets these variables:

| Variable | Values |
| --- | --- |
| `JUKAHUB_IDLE_STATE` | `idle`, `extended` or `active` |
| `JUKAHUB_IDLE_WHERE` | `game` (a game is running) or `home` |
| `JUKAHUB_IDLE_SOURCE` | what caused it, for your own logs: `input`, `idle screen`, `sleep: power-button`, `wake: game-menu`… |

When they run:

| Where | `idle` | `extended` | `active` |
| --- | --- | --- | --- |
| In a game (with **Dim when idle in games** on) | the screen dims | just before the sleep or power off (the action waits up to 15 s for the scripts) | the first input after the dim, the wake, or the game ending while dim |
| On Home | the idle screen or the clock dock comes up | — | the idle screen goes away |
| Any sleep (power button, in-game menu, Auto sleep, the in-game idle) | — | before the sleep (it waits under a second for the scripts, then carries on) | after the wake |

A state only runs when it changes, per place: a sleep that the in-game idle
started does not run the `extended` scripts a second time. Nothing runs on
the power-off that **Power off after sleeping** does. The scripts never run
on the screen's thread, so a slow script never freezes JukaHub. Nothing runs
while JukaHub's startup lock is up: the scripts wait for the unlock, and a
state that changed again meanwhile is skipped.

Example — a log line for every change; put the same file in all three
folders (`hooks/idle/10-log.sh`, `hooks/extended/10-log.sh`,
`hooks/active/10-log.sh`):

```sh
#!/bin/sh
echo "$(date) $JUKAHUB_IDLE_STATE in $JUKAHUB_IDLE_WHERE ($JUKAHUB_IDLE_SOURCE)" >> /tmp/idle-hooks.log
```

Hooks run on the handheld only (Linux); on Windows the folders are ignored.

## Battery in games

Settings → Power & battery → **Battery in games**:

| Stop | What RetroArch shows |
| --- | --- |
| Off (default) | nothing |
| Low battery only | once each at 20, 15, 10 and 5% while the battery drains |
| Every 15 min | the level every 15 minutes, and the low-battery lines |
| Every 5 min | the level every 5 minutes, and the low-battery lines |

A line reads `Battery 54% · about 1 h 20 min left` — the same estimate as
Quick Settings, so a guess says "roughly" and a charger says "full in
about …". At 5% it adds "plug in now". A game started below a threshold says
so once, and a threshold the level climbs back over on a charger can fire
again later. **Show the time too** (off by default) starts the line with the
clock: `14:05 · Battery 54% · …`. Quiet Mode holds the interval lines, not the
low-battery ones.

It works for RetroArch games started from JukaHub: JukaHub turns RetroArch's
network commands on for that game only and sends the line with `SHOW_MSG`,
the way calendar reminders already appear. Other emulators cannot show a
line from outside. The line appears only with RetroArch's on-screen
notifications on: Settings → **In-game overlay** → Quiet turns them off. Nothing is
sent while the game is paused (the in-game menu, the game assistant, an
alarm, a sleep); a low-battery line that came due meanwhile appears a moment
after the game runs again.

The low-battery guard (**Save and suspend at 3%**) is unchanged. While
Battery in games' lines run for a game, they take the place of the
low-battery warning's own in-game line (**Show over games**) down to their 5%
line: RetroArch shows one battery message, not two, and only the lights
still flash for that warning. Below 5%, and in a RetroArch game JukaHub did
not start, the warning's own line ("Battery 4%") appears as before.

## How it works

- [player/idlewatch.go](../player/idlewatch.go): the settings, the per-game
  session (started and ended by the launch hooks), the timing decision, the
  second stage (`devSleepNow` / `devPowerRequest`) and the hook queue.
- [player/idlewatch_linux.go](../player/idlewatch_linux.go): the input
  devices (`/proc/bus/input/devices`, `/dev/input/event*` opened read-only
  and never grabbed, axis ranges from `EVIOCGABS`), the dim (the panel's raw
  level through `/dev/disp` or the sysfs backlight, read before and put back
  after) and the scripts (`/bin/sh`, own process group, killed at the
  timeout).
- [player/gamehud.go](../player/gamehud.go): Battery in games.
- One line in [player/saver_power.go](../player/saver_power.go) tells the
  hooks when the idle screen comes and goes; the sleep and wake use
  [player/sleephooks.go](../player/sleephooks.go).
