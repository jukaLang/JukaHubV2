# Text and hex editor

**Files** opens every file that has no viewer of its own in the built-in text
editor: a launch script, a RetroArch `.cfg`, a `packages.json`, a Python
helper, a log. A file that is not text opens in the hex editor instead of as a
screen of garbage, and **Y → Open with… → Hex editor** shows the bytes of any
file, text or not ([Open with](OPEN_WITH.md)). Both are the same full-screen
page: the file's name and position at the top, the content in the middle, the
keys the page answers in the footer.

## The text editor

The editor works a line at a time, which is what a D-pad and an on-screen
keyboard are good at: **A** opens the line under the cursor on the keyboard,
already filled in, so a change is a correction rather than a retype; **X**
inserts a new line after it; **R2** deletes it. Nothing is written until
**Y**. A save goes to a temporary file beside the original that is then
renamed over it, so a power cut leaves the old file or the new one, never half
of each; it keeps the file's permissions; and a `.json` file must be valid JSON
first, because a broken `packages.json` would break the very feature it
configures. **B** with unsaved changes asks once more before it throws them
away.

A file over 1 MB opens read-only and paged, so a 200 MB log costs a quarter of
a megabyte of memory however far into it you scroll; a large `.log` opens at
its end. A file JukaHub cannot write opens read-only too, and says so.

| Pad | Keyboard | Action |
|-----|----------|--------|
| Up / Down | Up / Down | Move a line |
| L1 / R1 | PgUp / PgDn | Move a page |
| L2 / R2 | Home / End | First / last line (R2 deletes the line in a file you can edit) |
| Left / Right | Left / Right | With word wrap off, scroll sideways; with it on, page |
| A | Enter | Edit the line on the keyboard |
| X | X / Insert | Insert a line after it |
| R2 | Delete | Delete the line |
| Y | Y / Ctrl+S | Save |
| Start | Tab | View options |
| – | W / N / H | Word wrap / line numbers / syntax colours |
| – | + / − | Text size |
| B | Esc | Close |

### Syntax colours

Code and configs are coloured: keywords, types and builtins, strings,
numbers, comments, the names of functions where they are defined or called,
variables, and the lines that are not code at all (an `#include`, a
`@decorator`, a `[section]`). The colours follow the theme, light or dark, and
stay readable under **Increase contrast**.

| Language | Files |
|----------|-------|
| Shell | `.sh`, `.bash`, `.zsh`, `.ksh`, `.bashrc`, `.profile` … and scripts whose first line is `#!/bin/sh`, `#!/usr/bin/env bash` … |
| Perl | `.pl`, `.pm`, `.t`, `.pod`, `#!/usr/bin/perl` |
| Python | `.py`, `.pyw`, `.pyi`, `#!/usr/bin/env python3` |
| JavaScript | `.js`, `.mjs`, `.cjs`, `.jsx`, `.ts`, `.tsx`, `#!/usr/bin/env node` |
| JSON | `.json`, `.jsonc`, `.json5`, `.geojson`, `.jsonl` |
| C/C++ | `.c`, `.h`, `.cpp`, `.cc`, `.cxx`, `.hpp`, `.ino` … |
| Java | `.java` |
| Config | `.ini`, `.cfg`, `.conf`, `.toml`, `.yaml`, `.yml`, `.properties`, `.env`, RetroArch's `.opt`, `.rmp`, `.cht`, shader presets |

What spans lines is followed from the top of the file: a `/* … */` comment, a
Python docstring, a JavaScript template string, a shell or Perl here-document,
Perl's POD and everything after `__END__`. The finer guesses — whether a `/`
starts a regular expression or divides, whether a Perl `%` is a hash or the
modulus, whether a shell word is a command — are made from the token before
them, as desktop editors do; a wrong guess only miscolours the rest of that
line.

**View → Syntax colours** (or **H** on a keyboard) switches the colours off
and on for every file. **View → Highlight as** colours the open file as
another language — Left / Right step through them — for a script with no
extension and no `#!` line; the choice lasts until the file is closed. A line
holding a character the font cannot draw is shown in one colour.

The keyboard does not capitalise the first letter of a line of code or
config the way it does in a text file, so `import os` stays `import os`.

## The hex editor

The hex editor shows a file as offsets, bytes and their printable characters:
sixteen bytes a row, or eight when the text size or the screen leaves no room.
The header gives the cursor's offset, the file's size and the byte under the
cursor in hex, in decimal and as a character. Changed bytes are drawn in the
warning colour and zero bytes dimmed.

It changes bytes where they are: a file never grows or shrinks here, since a
byte inserted into a save file or a ROM header moves everything after it.

| Pad | Keyboard | Action |
|-----|----------|--------|
| Left / Right | Left / Right | Move a byte |
| Up / Down | Up / Down | Move a row |
| L1 / R1 | PgUp / PgDn | Move a page |
| L2 / R2 | Home / End | First / last byte |
| A | Enter | Edit the byte under the cursor |
| – | 0–9, A–F | Type hex digits straight in |
| X | X | Type bytes on the keyboard |
| Y | Y / Ctrl+S | Save |
| Start | Tab | View: go to offset, undo, show as text |
| – | Ctrl+G / Ctrl+Z | Go to offset / undo |
| B | Esc | Close |

**Editing a byte with the pad.** **A** starts on the byte's high digit:
**Up / Down** count that digit up or down (F wraps round to 0), **Left /
Right** step to the other digit and on into the next byte, so a run of bytes
can be changed in one go. **A** keeps the changes; **B** puts back every byte
this edit changed. With a keyboard, typing a hex digit writes it into the
digit under the cursor and moves on.

**Typing bytes.** **X** opens the keyboard: type hex pairs (`4A 75 6B 61`,
`4a756b61` or `0x4A,0x75`) or text in quotes (`"Juka"`), and **Apply** writes
them over the bytes from the cursor on. What would run past the end of the
file is left out, and the toast says how many bytes were changed.

**Go to offset** (View, or **Ctrl+G**) takes a hex offset (`1F00` or
`0x1F00`) or a share of the file (`50%`).

**Undo.** **View → Undo the last change** (or **Ctrl+Z**) takes back the last
press — a typed run of bytes is one — and **Undo every change** all of them.

**Saving.** Nothing is written until **Y**. A save first checks that the file
is still the one that was opened — the same size and the same time — and
refuses one that changed on disk meanwhile, because every change is an
offset into the old one. A file up to 64 MB is then written as a copy with
the changes and renamed over the original, like the text editor's saves; a
larger one (a disc image) has just the changed bytes written in place, so a
four-byte fix does not copy gigabytes or need their size again in free space.

For a binary file, **Text** in the toolbar (or **View → Show as text**) shows
its bytes as text — some "binary" files are logs with a stray NUL — and
**X** goes back. For a text file opened in the hex editor, **Text** reopens it
in the text editor. Either way, changed bytes have to be saved or undone
first.
