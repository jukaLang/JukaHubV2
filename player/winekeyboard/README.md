# Wine Virtual Keyboard

`keyboard.c` is a small, no-CRT, 64-bit Windows program. It starts the Windows
program named by `JUKAHUB_VK_TARGET`, then shows an always-on-top **Virtual
Keyboard** tab in the lower right of the Wine desktop. The handheld's **Start**
button sends F12 to this program, which opens or hides the keyboard even while
another Wine window has focus. Clicking a key sends Win32 `SendInput` events to
the active editor. **Hide** collapses it to the tab. The handheld's Wine mouse
bridge moves the pointer with a stick and
clicks with **A**. **Enter** is a key on the expanded keyboard. Select+Start
still sends Alt+F4, or ends the session when held.

`JUKAHUB_VK_TARGET` must be an absolute Windows path to an executable in the
same Wine prefix, such as
`Z:\mnt\SDCARD\Apps\JukaHub-Trimui\data\windows-apps\Notepad++\64-bit\notepad++.exe`.
The production program exits with code 3 if it is missing. It returns the
child's exit code when that process exits. `JUKAHUB_VK_CMDLINE` is the optional,
fully quoted Windows command line for that child (including its executable as
the first word),
passed to `CreateProcessW` in a writable buffer. JukaHub sets both variables
when it wraps a Windows App; app arguments and the original environment stay
with the child. JukaHub only wraps executables found by its Windows Apps
scanner and replaces any caller-supplied `JUKAHUB_VK_*` values. The helper has
the same device access as the Wine process it starts.

Build on x86-64 Linux with Python 3, GCC, and binutils (the `JukaHubBuild` WSL
distribution has these):

```sh
python3 player/winekeyboard/mkkeyboard.py
```

This writes reproducible `keyboard.exe.gz` beside the source. `--keep` also
writes the uncompressed PE for a direct Wine test. `--out DIR` changes the
destination. The builder reuses the PE writer in `player/winetest` without
changing or overwriting that program's source or artifacts.

For a device proof, `--proof --keep --out DIR` produces a test build that uses
the sample Notepad++ path above when the environment variable is absent. The
proof build must not be embedded in JukaHub because it has a device-specific
fallback path. An on-device Notepad++ proof confirmed the panel renders and a
key press enters text in the editor.
