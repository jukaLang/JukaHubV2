# Recovery and resilience

These features keep a handheld usable when things go wrong: a low-battery guard
that gets a save state onto the card before the power dies, a recovery chord
that starts JukaHub in safe mode and offers the previous version, the last good
settings or a settings reset, a boot guard that notices failed starts by itself,
crash reports, and an SD-card health check that catches the card problems behind
most "the app broke" reports.

## Low-battery auto-save and suspend

When the charge drops to **3% while a game is running**, JukaHub:

1. asks RetroArch to write a save state (`SAVE_STATE`), waits briefly, then asks
   it to quit (`QUIT`) through RetroArch's network command interface;
2. waits up to 8 seconds for the emulator to close its files;
3. runs `sync` so nothing is left in the page cache;
4. suspends the device (`mem`, then `freeze`, whichever `/sys/power/state`
   supports).

At **5%** it warns instead, and only once per drop.

The guard never acts while charging or when the charge level is unknown, and it
only saves and suspends when a game is actually running: reading a note at 2%
should not put the device to sleep without warning. It acts at most once per
launch, so a failing card cannot turn into a loop. It never powers the device
off — a hard shutdown at an unknown point is what this feature exists to avoid.

### Enabling RetroArch network commands

RetroArch's command interface is off by default in most builds. Add to
`retroarch.cfg` (the file JukaHub reads first is `RetroArch/retroarch.cfg` on the
SD card; `JUKAHUB_RETROARCH_CFG` overrides the search):

```
network_cmd_enable = "true"
network_cmd_port = "55355"
```

Check it with any UDP tool; a listener answers on `127.0.0.1:55355`. JukaHub
reads the config itself, so the low-battery warning says when auto-save is off
and why. Without the interface the guard still syncs and suspends, and says that
no save state was written.

The feature is a toggle in **Settings → General → Smart Widgets → Save & suspend
at 3%** (on by default, variable `lowBatteryGuard`).

## Quitting, and the power button

On a TrimUI the app is usually stopped from outside: the firmware's power
handling signals the foreground app, or a launcher stops it. A process stopped
by a signal never runs its cleanup, and a game it launched keeps the frame
buffer — with the stock launcher already killed, the panel then shows a frozen
picture that no button leaves. That is the "it locked when I pressed power"
report.

JukaHub now treats those stops as a normal quit:

- `SIGINT`, `SIGTERM`, `SIGHUP` and `SIGQUIT` become the same exit request the
  Exit button uses, so favourites, playtime and LEDs are handled and the
  teardown card is skipped (the caller is waiting for the process, not watching
  it).
  A second signal quits immediately.
- A running game, emulator, port or Wine app is asked to quit (2 s), then
  killed, so nothing is left owning the display. Music and video decoders are
  stopped for the same reason, which used to leave audio playing after a quit.
- The whole shutdown is bounded to **6 seconds**. If a cleanup step blocks — a
  wedged server, a flush on a dying card, a child that ignores the request — the
  watchdog kills the children, drops the LEDs and exits anyway.
- When the watchdog fires it writes every goroutine stack to `errors.txt` first.
  Send that file with a report: the blocked shutdown names the lock or channel
  it was waiting on.
- `launch.sh` catches the same stop, waits for JukaHub to exit and still hands
  the screen back, so the stock launcher comes back even when the request
  arrived as a signal to the whole process group.

A short press of the power button is handled by the firmware (suspend), not by
JukaHub; a long press force-quits the app. Both now end with the display handed
back instead of a frozen last frame.

## Recovery chord (safe boot)

Hold **L + R + Start** (on a keyboard: **Ctrl + Alt + R**) while JukaHub starts.
The chord must stay down for the whole 1.2-second window, so a stray press
cannot trigger it.

When it triggers, safe mode turns on for this launch (plugins, updates, tool
installs and background network work are skipped) and the **Recovery** card
opens with the first frame. Nothing has been changed at that point; the card
offers:

| Choice | What it does |
| --- | --- |
| Previous version | Installs the version kept in Version history that was released just before this one, and restarts (the current one is kept there too, so it can be undone). On an install from before Version history, the build an older update kept in its backup set is put back after a confirmation and takes over at the next start (**Restart now** is offered). |
| Last good settings | Puts back the settings the last healthy start read (kept in `patch/lastgood/` once a start has run 90 seconds outside safe mode), else the settings saved before the last change (the `.bak` of `jukauser.json` / `jukaconfig.json`) or the newest weekly backup — the last good start first, then newest first. |
| Safe mode | Keeps going in safe mode until JukaHub restarts. |
| Reset settings (keep games and saves) | Settings and the home layout go back to how JukaHub ships; games, saves, favourites, play history, themes and plugins stay. |

The two settings choices ask first and always write a safety backup of the
current settings (`JukaHub-safety-*.jkbackup`, listed in Tools → Your setup →
Backup & restore) before anything changes; nothing changes if that backup
cannot be written. The card works with the D-pad and A/B, the arrow keys and
Enter/Esc, or a tap. The result appears in the log and in Diagnostics
(`Safe boot : ...`).

You can also choose **Settings → About → Restore all defaults** while JukaHub
is running. It asks for confirmation, then uses the same settings reset and
safety backup as the recovery card.

Safe mode can also be requested without the chord:

| Trigger | Use |
| --- | --- |
| `--safe-mode` | from a terminal or a launcher script |
| `JUKAHUB_SAFE_MODE=1` | export it before launching |
| `safe-mode.flag` next to the app | create it over USB or FTP |

Interrupted updates are separate: JukaHub replays the transaction journal at
startup and restores the files it replaced, without any user action.

## Boot guard (failed starts)

Every start is recorded in `patch/bootguard.json`. A start counts as
**healthy** once JukaHub has drawn its first frame and then kept running for
90 seconds, or when its main loop ends on a quit (recorded before the teardown
runs). Anything else — a crash, a kill, a hang, a power cut in the first minute
and a half — is a **failed start**. A crash after a long session is a healthy
start that ended badly and does not count. The guard starts before the config
is loaded, so a start that dies loading it counts too.

- **Three failed starts in a row**, whatever the update history, start safe mode
  by themselves, with a card that says why and how to leave it (quit and open
  JukaHub again; the next start is normal once one start is healthy).
- When those failures are the running build's own, began within its first ten
  starts, and at least one of them was a crash (a crash report was filed for
  it, or its exit status says so — see below), the build is treated as a bad
  update: the previous version — the one released before it — is put back from
  Version history and JukaHub restarts into it. A power cut, or a quit that had
  to be forced, in a session that was working never rolls an update back.
- A loop gets **one** automatic rollback: if the restored build fails its starts
  too, the cause is probably not the update, and the guard starts safe mode
  instead of going back again. The next start outside safe mode that runs 90
  seconds or quits cleanly makes rollbacks possible again.

**How a start ended.** A start that was asked to stop (the power button, a stop
signal, Quit) is not a failed start, even when the stop has to be forced during
a slow splash, unless it crashed on the way out. On the handheld, `launch.sh`
leaves each run's exit status in `.last-exit` beside the app, and the next start
reads and deletes it: 1 (a fatal error), 2 (a crash) and a signal such as 134
(abort), 137 (killed, usually by the out-of-memory killer) or 139 (segfault)
count as a crash, except the stop signals (129, 130, 131, 143) and a kill that
followed a stop request; 0 never does. Without that file (Windows, a desktop,
an older `launch.sh`), a start that died before its first frame counts as a
crash.

The count is in starts, not in clock time: the handheld may have no real-time
clock, so after a cold boot its clock can sit at a factory date or jump ahead
once the network sets it. `JUKAHUB_NO_BOOTGUARD=1` turns the guard off for
debugging; a safe-mode flag, variable or command line keeps its automatic
rollback away, and so does holding the recovery chord: the Recovery card then
says the last starts failed and offers **Previous version** first.

## Crash reports

Each session writes a one-line header to `crash-session-<pid>.txt` in the log
folder and hands the file to the Go runtime as its crash output, so a panic on
any goroutine or a fatal runtime error is written there as well as to
`errors.txt`. At the next start a session file with anything after its header
becomes `crash-<yyyymmdd-hhmmss>.txt` (the newest five are kept) and JukaHub
shows **JukaHub closed unexpectedly** with:

- **Send report** — opens Report a bug with the crash category, a title and the
  crash excerpt filled in; you see exactly what would be sent and nothing leaves
  the device until you press Submit;
- **Restart in safe mode** — the next start (right away where JukaHub can
  restart itself) is in safe mode;
- **Recovery options** — the recovery card above.

With the recovery chord held, the Recovery card offers **Send report** for a
crash report filed at that start instead.

| Platform | Log folder |
| --- | --- |
| TrimUI | the app folder, next to `errors.txt` |
| Windows | `%LOCALAPPDATA%\JukaHub\logs` (created on demand) |
| Linux desktop | `data/logs` next to the binary |

A panic exits with status **2** (a clean quit is 0), and `launch.sh` writes
`JukaHub CRASHED` into `errors.txt` for it; it does not restart a crashed
JukaHub. A panic on the main goroutine still runs the cleanup (the running game
is stopped, the LEDs and the CPU governor are put back), bounded to 5 seconds so
a crash can never leave a frozen frame.

## SD-card health

**Diagnostics** now reports the card JukaHub runs from:

```
SD card      : exfat, 58.2 GiB total, 41.7 GiB free
SD write     : 21.4 MB/s (read back verified)
```

The write figure is measured, not quoted: JukaHub writes a 2 MiB deterministic
block into its own state directory, syncs it, reads it back and deletes it. It
reports warnings for:

- a FAT32 volume (no journal, no files over 4 GB);
- less than 512 MiB free, which an update needs for its staged copy;
- a measured write speed under 2 MB/s, where saves and video playback stall;
- a block that does not read back unchanged, the signature of a failing card;
- a filesystem that reports more free space than capacity.

The report is cached for 10 minutes, so opening Diagnostics twice does not write
to the card twice. It contains no paths and no card identifiers, which keeps it
safe to paste into a bug report.

A deep fake-capacity test (filling the card until writes fail) is **not** run
automatically: on a card that already holds saves it is a data-loss operation,
not a diagnostic. Use a dedicated empty card and a tool such as `f3` for that.
