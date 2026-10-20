# Recovery and resilience

Three features keep a handheld usable when things go wrong: a low-battery guard
that gets a save state onto the card before the power dies, a recovery chord that
starts JukaHub without its customisations and rolls back the last update, and an
SD-card health check that catches the card problems behind most "the app broke"
reports.

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

## Recovery chord (safe boot)

Hold **L + R + Start** while JukaHub starts. The chord must stay down for the
whole 1.2-second window, so a stray press cannot trigger it.

When it triggers:

- safe mode turns on for this launch: plugins, updates, tool installs and
  background network work are skipped;
- the previous JukaHub binary is restored from the newest backup set that
  contains it;
- the result appears in the log and in Diagnostics (`Safe boot : ...`).

The config is deliberately left alone. A backup set can be old, and silently
reverting settings made after the update would be its own bug; the executable is
the file an update replaces. Restart JukaHub after the chord to run the restored
build.

Safe mode can also be requested without the chord:

| Trigger | Use |
| --- | --- |
| `--safe-mode` | from a terminal or a launcher script |
| `JUKAHUB_SAFE_MODE=1` | export it before launching |
| `safe-mode.flag` next to the app | create it over USB or FTP |

Interrupted updates are separate: JukaHub replays the transaction journal at
startup and restores the files it replaced, without any user action.

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
