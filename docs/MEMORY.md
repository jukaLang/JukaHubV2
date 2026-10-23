# Memory scanner

The Memory scanner finds a value in a running game's RAM and writes a new one
back. It is the tool a cheat table is built with: search for a number you can
see on screen — health, ammo, coins — change it in the game, narrow to the
addresses that followed the change, then write the value you want.

Open **Tools → Power tools → Memory scanner**.

It needs no root, no PC and no downloaded cheat database. Everything happens on
the device, against one process you pick.

## The flow

1. **Pick a process.** The picker lists the running processes, busiest first.
   **A** attaches to the highlighted one. JukaHub refuses its own process: it
   would freeze the panel that is drawing the scan.
2. **Enter the value** you can see in the game (**Value → A**).
3. **Scan.** The first pass walks every readable, writable mapping of the
   process and records each aligned address that holds the value at the chosen
   width. A four-byte `100` matches a great many addresses.
4. **Change the value in the game**, then **Narrow**. Only the addresses that
   now hold the new number survive. Two or three rounds usually leave one.
5. **Browse the addresses** and watch the live value column while you play.
   The value that moves with the game is the one you want.
6. **Write.** With the address selected, enter the number you want and press
   **A** twice: the first press arms the address, the second writes it.

## What the scan sees

| Width | Holds |
|-------|-------|
| 1 byte | 0 – 255, and negative values as two's-complement bytes |
| 2 bytes | 0 – 65535 (two's complement for negatives) |
| 4 bytes | 0 – 4294967295, the width most games use for counters |
| 8 bytes | full 64-bit counters and pointers |

Values are little-endian, which is what every ARM and x86 handheld stores. They
are matched at aligned offsets only — an unaligned match is almost always a
coincidence or part of a larger field.

Only read-write mappings are scanned. Code, read-only data and file mappings are
skipped: they cannot hold the number a game decrements as you spend ammo.

Changing the width drops the current scan, because the addresses of a two-byte
search mean nothing to an eight-byte one.

## What it does not do

- **No float or double search yet.** Only integers. A health bar stored as
  `float32` is not found by this panel.
- **No "changed / unchanged / increased" comparisons.** Only "equals".
- **No pointer scanning** and no cheat-table files. Addresses are absolute, so
  they break when the game reallocates memory or you restart it.
- **No multi-thread group stop.** The attach stops the process's main thread;
  other threads of a multi-threaded game keep running. Reads and writes still
  work because the address space is shared, but a game that keeps updating from
  another thread can overwrite a written value immediately.
- **No write to read-only mappings** — the kernel refuses, and the panel shows
  the error.

## Permissions and safety

Reading another process needs `ptrace` permission, the same as any debugger.
On the handheld that is normally available to an app running as the same user.
A refusal is reported as "cannot read that process" and nothing is changed.

Attaching stops the target. The panel detaches when it closes, so the game
resumes, and the candidate list survives — which is what makes "scan, play until
the value changes, narrow" work without repeating the first scan.

Nothing is written to the target's memory until the two-press confirmation. A
write cannot be undone by closing the panel: the game keeps the new value until
it overwrites it itself. Save first if the value matters.

Writes only touch memory the game has already written to. Another app's memory
is never opened unless you pick it in the picker.

## Limits

- A first scan of a small common value can match more than a million addresses.
  The scan stops at that point and asks for a more specific value rather than
  holding the list on a handheld.
- The process list reloads every three seconds while the picker is open; R
  forces it.
- Linux only. Other platforms build the panel and its arithmetic (the tests run
  everywhere) but report that process memory is unavailable when a target is
  picked.

## Related

- [Power tools](CONTROLS.md#power-tools) for the panel it lives in.
- [Controls](CONTROLS.md#memory-scanner) for the button table.
- [Save Manager](SAVE_MANAGER.md) for editing what is on the SD card instead of
  what is in RAM.
