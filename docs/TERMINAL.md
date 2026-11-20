# Terminal

JukaHub's Terminal (Start menu → Screens → Terminal, or Tools → Terminal) runs
commands on the device: its own built-ins (`ls`, `cd`, `cat`, `sysinfo`,
`neofetch`, `calc`, `ssh` …, see `help`) and everything else through the
system shell (`sh` on the handheld and Linux, `cmd.exe` on Windows). History is
kept in `terminal_history` in the data folder.

Typing a command with a D-pad is slow, so the Terminal has three ways to avoid
it: the **Command Deck**, **Favorites and macros**, and **token chips** above
the on-screen keyboard.

Code: `player/terminal*.go`, `player/cmddeck*.go`; interactive programs
`player/termpty*.go` and the emulator `player/termvt.go`.

## apt (Linux packages on the handheld)

On a TrimUI, `apt` (also `apt-get` and `jpkg`) is a built-in: the package
manager of [Linux packages](LINUX_PACKAGES.md), which installs Ubuntu 24.04's
arm64 programs onto the card.

```
apt update                 # Ubuntu's signed package index (about 20 MB the first time)
apt install python3 git    # what they need comes along
apt search sqlite          # name and description
apt show nmap
apt list --installed       # or --upgradable
apt upgrade                # newer versions of what is installed
apt remove git             # and apt autoremove for leftovers
apt clean                  # delete downloaded .deb files
```

It runs as a Terminal job: its lines stream in, **X** / **Ctrl+C** stops it
(the next install repairs a package that was half unpacked), and it may run
for up to two hours instead of the five minutes a shell command gets. Leaving
the Terminal stops it, as it stops any command. A command that brings in
packages you did not name, or an upgrade, prints what it would do and changes
nothing: the Terminal cannot answer a question while a command runs, so the
same command with `-y` is typed into the prompt for you — press **Start** to
go ahead.

Every other command the Terminal runs has `<data>/linux/bin` first on its
`PATH`, so installed programs (`python3`, `perl`, `git`, `nmap` …) are found by
name; firmware tools keep the firmware's libraries. A line with shell syntax
(`apt search x | grep y`) goes to the shell, where `<data>/linux/bin/apt` runs
`JukaHub apt …`. The same `apt` works in an SSH shell on the device (call
`<data>/linux/bin/apt`, or `. <data>/linux/env.sh` first to get the programs on
`PATH`); there it asks *Do you want to continue? [Y/n]* like apt.

On Windows and on a Linux PC, `apt` says it is the handheld's package manager;
a PC with its own `apt` on `PATH` runs that one instead.

## Java and input to running programs

`java`, `javac`, `jshell`, `jar` and `javaw` are built-ins too
([JAVA.md](JAVA.md)). They use the Java JukaHub installed (Tools → Java), or
the computer's own Java when it has none.

```
java status                # which Java is used, and the others found
java install               # Java 25, the JRE (about 50 MB)
java install 21 jdk        # any of 8, 11, 17, 21, 25; jre or jdk
java update                # a newer build of the same version, if there is one
java remove                # delete JukaHub's Java
java -version
java -jar Game.jar
java Hello.java            # straight from the source (needs the JDK)
javac Hello.java && java Hello
```

A Java line without shell syntax runs directly, and **the program reads what
you type**: while it runs, Enter (Start on a pad, or the on-screen keyboard's
Enter) sends the line to it instead of running a command — an empty line too.
The prompt's chip says **Input** and the footer shows Send. What the program
asked on its line and your answer stay together on one line, as on a real
terminal. **Ctrl+D** ends its input (end of file); X / Ctrl+C stops it.
Leaving the Terminal stops the program, as it stops any command there; a Java
program may run for up to 6 hours.

A line with pipes, redirects or variables (`java -jar tool.jar > out.txt`)
goes to the shell; JukaHub's Java is first on that shell's `PATH` and
`JAVA_HOME` is set, but such a program gets no typed input. Every `java` run
gets UTF-8 output, and on the handheld `java.awt.headless=true` (it has no
desktop). On Windows `javaw` starts the program on its own, without a console.

The Command Deck has a **Java** category with these commands, and Files runs
a `.jar`, `.java` or `.class` here when it is a text program.

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

## Interactive programs

The line terminal runs one command per line through a pipe, which full-screen
and interactive programs refuse. Those get a real terminal instead — a
pseudo-terminal on the handheld and on Linux, a ConPTY pseudo console on
Windows 10 (1809) and newer — drawn by JukaHub's own VT100/xterm emulator:

```
shell                     # an interactive shell: bash when there is one (Linux packages' too), else sh; cmd.exe on Windows
bash                      # that shell exactly (also sh, zsh, fish, cmd, powershell, pwsh, wsl) — "not found" when missing
htop                      # also top, vi/vim, nano, less, more, man, mc, tmux, screen, watch, ncdu …
dmesg | less              # a pipeline that ends in a pager
python3                   # a REPL when typed with no arguments (also lua, node, bc, sqlite3 …)
ssh user@host             # also ssh -p 2222 host, ssh user@host:2222
ssh forget host           # drop a host key JukaHub remembers
```

The Command Deck has **Interactive shell** (System) and **Live process
monitor** (Processes). A program that is missing says how to get it (`apt
install htop`). `ssh on|off|status` is still JukaHub's own SFTP server.
`htop -V`, `vim --version` and the like print in the line terminal; flags
that keep a program interactive (`nano -v`, `mc -v`, `vim -V`) open a session.
JukaHub's own commands stay its own in a pipeline (`help | less`).

The session fills the screen under a slim header (the program's title, how
long it has run, a **Ctrl** chip while R2's Ctrl is armed). Text is drawn with
a monospace face when the system has one — DejaVu Sans Mono, Liberation Mono,
Noto Sans Mono, Consolas, Cascadia Mono, or any `*Mono*.ttf` copied into
JukaHub's `fonts` data folder — and with the UI face otherwise, each character
centred in its cell so columns stay straight. Box drawing, block elements and
braille are drawn as shapes. The emulator handles colours (16, 256 and 24-bit),
the alternate screen, scroll regions, line drawing, wide characters, bracketed
paste and window titles, and keeps 1,000 lines of scrollback on the shell's
screen. Programs see `TERM=xterm-256color` when the device has that terminal
description (else `xterm`, else `vt100`, which BusyBox's tools are happy
with), `LANG=C.UTF-8`, and the same `PATH` as the line terminal (Linux
packages and Java first). The size comes from the terminal itself, never
from `COLUMNS`/`LINES` (dropped from the environment), so programs follow a
text-size change.

### Controls

| Pad | Keyboard | Does |
|---|---|---|
| D-pad | arrows | arrow keys (in the mode the program asked for) |
| A | Enter | Enter |
| B | Esc | Esc |
| hold B (0.8 s) | Ctrl+Shift+Q | leave the session — it keeps running |
| X | Backspace | Backspace |
| Y | — | on-screen keyboard; each character is sent as it is typed, Enter sends Enter and closes it, B closes it; the grid moves up so the cursor's line stays above it, and while the program asks for a password (sudo, su, ssh) the field shows dots and the suggestion strip hides |
| L2 | Tab | Tab |
| R2 | — | Ctrl for the next key (a letter typed on the keyboard, an arrow, a strip key) |
| L1 / R1 | PgUp / PgDn | in a full-screen program: PgUp / PgDn; on the shell's screen: scroll back a page |
| — | Shift+PgUp / PgDn, mouse wheel | scroll back (the wheel is the arrows inside a full-screen program) |
| Select | — | special keys: Esc, Tab, Ctrl+C, Ctrl+D, Ctrl+Z, Ctrl+X, Ctrl+O, Ctrl+L, Ctrl+R, Home, End, PgUp, PgDn, F1–F12, `q`, `:wq` |
| Start | Ctrl+Shift+M, or a click on the title bar | session menu: Leave, End session, Paste, Smaller / Larger text, Keys help |
| — | Ctrl+Shift+V | paste |
| — | Ctrl+Shift+Plus / Minus | text size |

With a keyboard every other key goes to the program: F1–F12, Ctrl+Q, Ctrl+L,
Ctrl+C, Ctrl+V and F4 do what the program says, not JukaHub's global
shortcuts. Left Alt sends Meta (Esc prefix before the character the key
types, so Alt+Shift+1 is Esc !); AltGr types characters. The Ctrl+Shift
chords go by the key's place on a non-Latin layout (Cyrillic, Greek). The
TrimUI Menu button (voice), Select + Start (quit JukaHub) and the alarm keep
working as everywhere.

Leaving keeps the program running: the line terminal shows a **Start resumes
…** pill (Ctrl+Shift+Q on a keyboard, or `shell` again), and Start on an empty
prompt resumes (unless a Java program in the line terminal is waiting for
input — then Start answers it and the pill resumes). One session runs at a time. When its program exits — `exit`
in the shell, `q` in htop — the Terminal goes back to the line view, keeping
the last screen's text and how it ended. **End session** stops it and
everything it started (the whole session's processes on Linux, a job object
on Windows), and so does quitting JukaHub.

### The built-in SSH client

When the system has an `ssh` program (Windows' OpenSSH, Dropbear, or `apt
install openssh-client`), `ssh …` runs it, with every option it knows. Without
one JukaHub connects itself: `ssh [-p port] [-l user] [user@]host[:port]`
opens a shell on the remote computer (the user defaults to the device's). It
signs in with the password or keyboard-interactive questions typed in the
session (a password is not shown), or with an unencrypted `~/.ssh/id_ed25519`,
`id_ecdsa` or `id_rsa`. Keys pressed while it is still connecting are dropped
rather than answering the first question, and the server's banner and
questions are shown without their control sequences. End session stops a
connection that is still being made at once. The remote terminal follows the
session's size.

Host keys are trusted on first use: the first connection shows the key's
fingerprint and asks *Trust it and connect? (yes/no)*; the key is then kept in
`terminal_known_hosts` in the data folder. A host that later shows a different
key is refused with a warning — if the server was reinstalled, `ssh forget
host` drops the old key and the next connection asks again.

## Status

Written 2026-11-17 without a Go toolchain: statically checked, not yet built,
and unverified on device — in particular the chip strip's
width on the Brick's 960 px canvas, `PATH` lookups on TrimUI's busybox, and the
PortMaster / RetroArch paths the deck probes.

Interactive programs were written 2026-11-19, the same way: statically checked
for linux/arm64 and windows/amd64, the emulator covered by one table test, not
yet built or run. The PTY on the handheld, the ConPTY path on Windows, the
monospace font search and the speed of a large `cat` need the device.
