# Terminal

JukaHub's Terminal (Start menu → Screens → Terminal, or Tools → Terminal) runs
commands on the device: its own built-ins (`ls`, `cd`, `cat`, `sysinfo`,
`neofetch`, `calc`, `ssh` …, see `help`) and everything else through the
system shell (`sh` on the handheld and Linux, `cmd.exe` on Windows). History is
kept in `terminal_history` in the data folder.

Typing a command with a D-pad is slow, so the Terminal has three ways to avoid
it: the **Command Deck**, **Favorites and macros**, and **token chips** above
the on-screen keyboard.

Code: `player/terminal*.go`, `player/cmddeck*.go`.

## Command Deck

Open it with **Select** in the Terminal (**Ctrl+P** on a keyboard, or the list
button beside the keyboard button in the prompt). It is also listed as
**Commands** in the Tools hub, the Start menu's Tools group and Home search,
which open the Terminal with the deck on top; closing it without running
anything returns to where you were. Home search also finds each command by
name ("free space", "ping", "kernel messages").

The rail on the left holds the categories:

| Category | What is in it (handheld / Linux) | Windows |
|----------|----------------------------------|---------|
| Favorites | Your saved commands and macros, plus "New macro from history…" | same |
| Ask Juka | "Why won't my last game start?", "What's filling my SD card?", "Make games run smoother", "Check my Wi-Fi" — Juka answers in Chat's Juka AI tab, using its device tools | same |
| System | uptime, memory, CPU governor and speed, temperatures, battery, kernel, OS release, `sysinfo` | uptime, version, memory, CPU load, battery |
| Storage | `df -h`, size of a folder, biggest items in a folder, mounts, `sync` | free space, folder size, biggest files (PowerShell) |
| Network | ping (1.1.1.1, google.com, your router, 8.8.8.8), IP addresses, Wi-Fi link, routes, DNS servers, name lookup, listening ports | ping, `ipconfig`, `netsh wlan`, routes, `nslookup`, ports |
| Files | list a folder, find by pattern, recently changed, count files, show a text file, delete a file | `dir`, find, `type`, `del` |
| Processes | `ps`, busiest processes, stop / force-stop a process | `tasklist`, `taskkill` |
| Logs | kernel messages, end of a log, search a log, system log | end of a log, search a log, recent system errors |
| Emulators | RetroArch cores and drivers, emulator folders, saves and states, the last game's log | RetroArch cores and drivers, game logs |
| PortMaster | PortMaster folder, its log, port runtimes | — |
| JukaHub | device card, JukaHub folder, data size, `errors.txt`, history | folder, data folder, history |

Only commands that can run on this device are listed: the deck looks each
program up on `PATH` once (and again after three minutes), checks the files
and folders a command reads, and hides a category with nothing in it. A
command that has a better twin on this firmware (`ip` over `ifconfig`, `iw`
over `iwconfig`) is listed once.

### Filling in a command

A command with blanks opens a form: the exact line that will run is shown at
the top and updates as you change the blanks. Each blank is filled with a
picker, never by typing:

| Blank | Picker |
|-------|--------|
| Folder | A browser that starts at the card (Home on a PC), the game-log folder or wherever the blank points. The first row uses the folder on screen (also **Y**); **A** enters a folder, **Left** goes up; the top level lists Files' Locations and your recent folders. |
| File | The same browser listing files; a log blank shows logs and text files only. |
| Number | **Left/Right** ±1, **L1/R1** ±10 right on the form; **A** offers presets. |
| Choice | **Left/Right** cycles; **A** lists the choices and your recent picks. |
| Process | The running processes, busiest first (init and JukaHub itself are left out); the command gets the PID. |

The last folder, file and choice you used for a blank come back next time.
A file or process blank with nothing to start from opens its picker at once.

Every value is quoted for the shell it goes to, so a file named
`$(reboot).zip` is a file name, not a command. On Windows a name containing
`%` or `"` is refused (cmd.exe would expand it even inside quotes); use the
prompt for such files.

Commands that change things — delete a file, stop or force-stop a process —
show a confirm card first. So does a favorite or macro whose line deletes or
stops something (`rm`, `del`, `kill`, `taskkill` …).

### Controls

| Pad | Keyboard | In the list | In a form |
|-----|----------|-------------|-----------|
| Up / Down | Up / Down | Choose a command | Choose a blank |
| Left / Right, L1 / R1 | Left / Right, Tab, PgUp / PgDn | Previous / next category | Change the value (±1, next choice); L1/R1 and PgUp/PgDn ±10 |
| A | Enter | Open (a command with blanks) or run | Pick a value |
| Start | Ctrl+Enter | Run now with the last values | Run |
| Y | Ctrl+E | Put the command in the prompt to edit | Same |
| X | Ctrl+S | Save to Favorites (remove one, in Favorites; Delete too) | Save to Favorites |
| B / Select | Esc / Ctrl+P | Close | Back to the list |
| — | typing | Search every category | — |

The mouse works too: click a category, click a row to select it and again to
open it, click a blank to pick its value, click outside the card to close.
A run lands in the Terminal; a command that is still running must be stopped
(X / Ctrl+C) before the deck runs another.

## Favorites and macros

- **Save** any deck command with **X** (Ctrl+S): it is saved with the values
  you picked.
- **Pin** any history entry from the history picker (**Y** in the Terminal,
  then **Y** on the entry; Ctrl+R then Ctrl+S on a keyboard).
- **New macro from history…** (first row group in Favorites) lists your last
  30 commands: **A** ticks, **Start** saves the ticked ones as one favorite.
  A macro runs its lines in the order they were run, joined with `&&`, so it
  stops at the first command that fails. The joined line goes to the system
  shell whole, so a `cd` in a macro applies to the rest of that macro only, and
  JukaHub's own built-ins (`sysinfo`, `neofetch`, `history`, `calc` …) cannot
  be part of one.

Favorites are kept in `terminal_favorites.json` in the data folder (at most 40,
up to 8 lines each, with the recent values of each blank). A file that cannot
be read is moved aside as `terminal_favorites.json.bad` and the deck starts
empty instead of failing.

## Token chips

While the on-screen keyboard types into the Terminal, the strip above the keys
offers whole tokens instead of English words, up to five:

- the next word of a command you ran before that starts the same way (on an
  empty line, whole recent commands);
- subcommands and flags for common commands (`ls -la`, `du -sh`, `tail -n`,
  `ping -c`, `ip addr`, `find -iname` …);
- paths for commands that take one (`cd`, `ls`, `cat`, `tail`, `du`, `find` …)
  or as soon as the word contains a slash. A folder ends in `/` and adds no
  space, so the next chip continues inside it; a name with spaces goes in
  quoted.

**Select** takes the first chip (the pad's Tab); **Up** from the keys walks
into the strip to take any of them; a click or tap takes the one under it. A
chip replaces the word at the caret and adds a space. With no chip, Select
closes the keyboard as before.

Voice typing (R2 / the mic chip) is now available in the Terminal too, since
the strip is no longer hidden there; what it types is not run until you press
Start.

## Status

Written 2026-11-17 without a Go toolchain: statically checked, not yet built,
and unverified on device — in particular the chip strip's
width on the Brick's 960 px canvas, `PATH` lookups on TrimUI's busybox, and the
PortMaster / RetroArch paths the deck probes.
