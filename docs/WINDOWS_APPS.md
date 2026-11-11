# Windows apps

**Windows Apps** starts Windows programs from JukaHub. On the TrimUI handheld
it runs them through Box64 and Wine inside WestonPack, a small Wayland/X11
display that PortMaster also uses; on a Linux PC with a desktop it uses Wine on
that desktop; on Windows it simply runs the program.

Open it from **Start → Windows Apps**, or from the pinned **Windows Apps** row
in **Emulators**.

> **Not verified on hardware yet.** The launch path described here (display,
> pad-as-mouse, clean-up) was written on 2026-11-11 without a device to try it
> on. It follows how PortMaster's own WestonPack ports start, but expect
> surprises on the first runs, and send the logs listed in
> [First run on a device](#first-run-on-a-device).

## What runs

| Kind of program | How it is shown | Expectation |
|-----------------|-----------------|-------------|
| Simple Win32 programs that draw their own windows (Notepad-class tools, small utilities, older 2D games) | WestonPack, **Windows (GDI)** mode | The target. |
| OpenGL 2 games | WestonPack, **OpenGL** mode (GLX on GLES through gl4es) | Experimental. |
| Direct3D games | Wine's own Direct3D; DXVK only where the Vulkan driver can run it | Mostly out of reach on the Smart Pro: its Vulkan is 1.1-level, and DXVK 2 needs 1.3. |
| Console programs | `display: "headless"` in a game profile | No window; output goes to the log. |

A Windows program does not become an ARM program: Box64 translates the x86
code and Wine provides Windows. On the Smart Pro's Cortex-A53 that is slow:
the **first launch** prepares the Windows folder (the Wine prefix) and takes a
few minutes; **later launches** still take tens of seconds before a window
appears. Programs that need anti-cheat, .NET, a Windows installer that pulls
more downloads, or a GPU feature the device lacks will not run.

## Starting a program

1. Copy the program into one of these folders (up to four folders deep):

   ```text
   data/windows-apps/
   /mnt/SDCARD/Roms/PC/
   /mnt/SDCARD/Apps/Windows/
   /mnt/SDCARD/Games/Windows/
   ```

   Wine's own `windows/`, `system32/` and `syswow64/` folders and uninstallers
   (`unins*.exe`) are skipped. Only files the scan found can be started.

2. Open **Windows Apps**, pick the program and press **A**.

The row says **Starting…** and its second line follows the launch: *Preparing
Windows…*, then what the runtime is doing (the first run creates the Windows
folder), then *Starting…*. Pressing **A** again on a starting program does
nothing. Closing the panel does not stop the launch or a running program. Once
the program takes the screen the panel stays as it was underneath; when the
program ends, JukaHub cleans up and the list is back.

One Windows program runs at a time. If the runtime is missing a piece, **A**
opens the setup page instead (see [Automatic setup](#automatic-setup)); while
the runtime is being installed or updated, **A** asks you to wait. A launch
that fails, or a program that closes with an error within about thirty
seconds, opens the *Couldn't launch* card with the reason and the log's name —
for a Wine game profile too. If another game took the screen while Windows was
being prepared, the launch stops there and that game keeps the screen and the
pad.

## Display modes

On the handheld every program runs inside WestonPack. **Y** on a row switches
that app between the two modes; the choice is kept per app in
`data/windows-apps/display.json`, and an OpenGL app says *OpenGL* on its row.

| Mode | westonwrap.sh mode | For |
|------|--------------------|-----|
| **Windows (GDI)** — the default | `drm gl kiosk system` | Programs that draw with GDI/Xlib: Weston's desktop is shown on screen through Crusty, WestonPack's SDL2 presenter. |
| **OpenGL** | `headless noop kiosk crusty_glx_gl4es` | OpenGL 2 games: the game's GLX calls are drawn on GLES through gl4es; Weston itself is not shown. A program that draws with GDI shows nothing here. |

On a Linux PC with an X11 or Wayland desktop, Wine uses that desktop and
WestonPack is not involved. Set `JUKAHUB_WINE_DISPLAY` to `auto`, `x11`,
`wayland`, `weston`, `weston-gl` or `headless` to force a choice for every
launch.

What JukaHub does around WestonPack, so a launch looks like a PortMaster port
to it:

* **The runtime image.** WestonPack ships as a SquashFS image. JukaHub mounts
  it read-only at `/tmp/jukahub-weston` (an existing mount of the same image,
  such as PortMaster's `/tmp/weston`, is used as it is) and unmounts it after
  the program, lazily if something still holds it. Mounting needs root, which
  JukaHub has on the TrimUI; when the system refuses, the launch says why. An
  unpacked `westonwrap.sh` (or `JUKAHUB_WESTON`) is used without mounting.
* **The command.** `westonwrap.sh <four mode words> NAME=VALUE… <Box64 + Wine>
  <program>` — the program's whole environment (`WINEPREFIX`, the Box64 and
  Wine settings, a profile's `env`) travels as `NAME=VALUE` words, because
  WestonPack hands the program only those. Afterwards `westonwrap.sh cleanup`
  runs, as in every WestonPack port.
* **PortMaster's values.** `westonwrap.sh` reads PortMaster's `control.txt`.
  JukaHub writes a small stand-in at
  `data/windows-runtime/weston-env/PortMaster/control.txt` and points
  `XDG_DATA_HOME` and `controlfolder` at it, with `DISPLAY_WIDTH` and
  `DISPLAY_HEIGHT` (1280×720 on the Smart Pro, 1024×768 on the Brick),
  `DEVICE_NAME`, `DEVICE_ARCH`, `ANALOG_STICKS`, an empty `ESUDO` (JukaHub runs
  as root) and `CFW_NAME=JukaHub`, plus `CRUSTY_SHOW_CURSOR=1` (Crusty draws the
  pointer) and `WESTON_HEADLESS_WIDTH`/`HEIGHT`.
* **Input devices.** The stock firmware's udev has no `input_id` rule, and
  Weston ignores every input device udev did not tag — it then does not start
  at all. Before the first Windows program after a restart, JukaHub adds that
  one rule as `/run/udev/rules.d/61-jukahub-input-id.rules` (RAM only), reloads
  udev and re-announces the **input** devices only (`udevadm trigger
  --subsystem-match=input --action=change`), the fix NX Redux ships for
  PortMaster's Weston ports on the same firmware.
* **A build that must not run.** CrossMix's preview of WestonPack
  ("Westonwrap 0.2.8 Preview 1") re-announces *every* device on a TrimUI, and
  the firmware then unmounts the SD card. JukaHub refuses a `westonwrap.sh`
  with such a bare `udevadm trigger` and asks for the official WestonPack. Such
  a copy does not count as installed: the setup page offers the official
  download instead.

### Trying other settings on a device

For testing without a new JukaHub build, put `NAME=VALUE` lines in
`data/windows-runtime/weston.env`. They are added to `westonwrap.sh`'s own
environment (over JukaHub's values), and two names change the mode words:

```text
# data/windows-runtime/weston.env
CFW_NAME=TrimUI
CRUSTY_RESOLUTION=640x360
JUKAHUB_WESTON_MODE=drm gl kiosk gl4es
JUKAHUB_WESTON_GL_MODE=headless noop kiosk crusty_x11egl
```

`JUKAHUB_WESTON_MODE` replaces the Windows (GDI) mode's four words,
`JUKAHUB_WESTON_GL_MODE` the OpenGL mode's. Lines that do not fit are ignored
and named in the app's log.

## Controls

### In the list

| Input | Action |
|-------|--------|
| D-pad Up / Down | Move through the programs |
| A / Enter | Start the selected program |
| Y / `y` / `d` | Display: Windows (GDI) or OpenGL, for this program |
| X / `x` / `h` | The setup page |
| Start / `r` / F5 | Rescan the folders |
| B / Esc | Close setup, then the launcher |
| Touch | Tap a row to start it; tap outside to close |

On the setup page, **Up / Down** picks a row and **A** installs it (after a
needs-runtime launch, **A** installs everything missing and then starts the
program, if the setup page is still open by then; leaving it lets the install
finish without starting anything).

### In a Windows program

On the handheld the pad is the program's mouse from the moment it starts — no
setting needed. JukaHub takes the pad for itself and adds a virtual device,
*JukaHub Mouse*, before WestonPack starts, so Weston finds it; the device stays
quiet until the program has started, so a press in those seconds cannot reach
JukaHub's own screen:

| Pad | In the program |
|-----|----------------|
| Either stick | Move the pointer |
| D-pad | Arrow keys (the Brick, which has no sticks, moves the pointer with it) |
| A | Left click; hold to drag |
| B | Right click |
| X | Middle click |
| L2 / R2 | Wheel up / down |
| Y | Esc |
| Start | Enter |
| L1 | Tab |
| R1 | Space |
| Select + Start | Alt+F4: close the window |
| Select + Start, held 2 s | End the program (when Alt+F4 did not) |
| Select + L1, held | Give the pad to the program as a gamepad; again to take it back as the mouse |
| Menu | Give the pad to the program |

The vibration motor pulses when the pad becomes the mouse and when it is given
back. An alarm that rings takes the pad back for JukaHub and returns it to the
program afterwards. There is no on-screen keyboard inside a Windows program, so
programs that need typing are out of reach for now.

## Logs

| Log | What it holds |
|-----|---------------|
| `data/windows-runtime/logs/<name>-<number>.log` | One per app: the runtime and its progress, the Windows folder, the display and how WestonPack was found or mounted, the input steps, the exact command line (values of names like `TOKEN`, `KEY` or `PASSWORD` hidden, and a game profile's own `env` values and arguments), everything WestonPack, Wine and the program printed, the exit and the clean-up. It is rotated past 1 MB. |
| `data/game-profiles/logs/<id>.log` | The same for a Wine game profile. |
| A bug report from JukaHub ([BUG_REPORTS.md](BUG_REPORTS.md)) | JukaHub's own recent log, with the `windowsapps` and `vcursor` lines. |

## First run on a device

1. Install the runtime (see [Automatic setup](#automatic-setup)) and copy one
   small Windows program into `data/windows-apps`: a portable GDI tool, or an
   old 2D game.
2. Start it and time it: the first launch prepares Windows for a few minutes.
3. Check, in this order: does a window appear; is there a pointer; do the
   sticks move it and does **A** click; do the D-pad, **Start** and **Y** act as
   arrows, Enter and Esc; does **Select + Start** close the window; does the
   list come back afterwards and does the pad work in JukaHub again?
4. Start it a second time: it should be quicker, and nothing from the first
   session should be left running.
5. Send back:
   * the app's log from `data/windows-runtime/logs/`;
   * the output of `cat /proc/bus/input/devices`, `cat /proc/mounts`,
     `ls -l /run/udev/rules.d /tmp/run/udev/rules.d` and `ps` while the program
     runs;
   * a JukaHub bug report.

If the screen stays black: try **Y** (OpenGL mode) for that app, then the
`weston.env` lines above one at a time (`JUKAHUB_WESTON_MODE=drm gl kiosk
gl4es`, then `CFW_NAME=TrimUI` — only with the official WestonPack).

## On Windows

The Windows build lists the programs in JukaHub's data folder
(`%APPDATA%\JukaHub\windows-apps`): `.exe` files, and `.bat` / `.cmd` files,
which run through `cmd.exe` with their path quoted safely. **A** starts the
program directly, in its own folder — no Wine, no WestonPack, nothing to set
up — and every row says **Ready**. The setup page, the display switch and the
pad-as-mouse do not apply: a PC has its own mouse and keyboard. JukaHub waits
off-screen while the program runs, as it does for games, and comes back when
it closes.

## Automatic setup

On the handheld, open **Windows Apps** and press **A** on a program (or press
**X** for Setup and **A** on the first row). JukaHub asks once, then installs
everything the program needs, in order, with progress on the setup page:

1. **Box64** — JukaHub's own build, attached to JukaHub's GitHub releases as
   `JukaHub-Wine-linux-arm64.zip` and built against an older glibc than the
   Smart Pro's 2.33, so the firmware can load it. It is checked against the
   release's signed manifest (or GitHub's digest) and its own `SHA256SUMS`, and
   started once (`box64 --version`) before it replaces anything.
2. **Wine 10.0** — Kron4ek's vanilla `wine-10.0-amd64-wow64` build (64- and
   32-bit Windows programs, no 32-bit Linux libraries needed). The download is
   checked against the SHA-256 and size the runtime pack names, and unpacked
   into `data/windows-runtime/wine-10.0`. Wine 11 is not used: it aborts on the
   Smart Pro's 4.9 kernel.
3. **WestonPack** — the PortMaster display runtime that shows Windows programs
   on a device without X11 or Wayland. If PortMaster has already downloaded it,
   that copy is used; otherwise JukaHub downloads it from PortMaster's
   repository (about 53 MB) and checks that it is a real SquashFS image.
4. **Windows itself** (the Wine prefix) — prepared once, without a display;
   this takes a few minutes under Box64.

Then the program starts. About 130 MB is downloaded and about 700 MB of
storage is used. The runtime is kept current by **Emulator updates** (Settings
→ Games & emulators), never while a Windows program is running.

**Where Windows lives.** A Wine prefix needs symbolic links, which an
exFAT/FAT card does not have. JukaHub uses, in order: `JUKAHUB_WINEPREFIX`; a
game profile's own `WINEPREFIX` (when it already holds a prefix or its folder
supports links); `data/windows-runtime/prefix` when the card supports links;
the Smart Pro's internal storage, `/mnt/UDISK/jukahub/wine-prefix` (needs 1 GB
free); otherwise a 1–2 GB ext4 image on the card,
`data/windows-runtime/prefix.img`, mounted only while a program runs. If none
of these is possible, the setup says so before downloading anything.

**Box64's own settings.** The wrappers in `data/windows-runtime/bin` hide
Box64's banner, keep its log quiet, point it at the pack's `box64.box64rc` and
switch its DynaCache off (Box64 0.4.4 writes up to 2 GB of translated code
otherwise, and a cache left over from an older Box64 or Wine has crashed
`wineboot`). A game profile can set any `BOX64_*` variable in its `env` —
`BOX64_DYNACACHE=1` turns the cache back on.

On a Linux PC, a Wine already on `PATH` is used. Without one, the first row
installs the same Wine 10.0 build natively (pinned to its published SHA-256).
On Windows nothing needs installing: `.exe` files run directly.

**Direct3D.** Wine's own Direct3D (wined3d) is the default. DXVK is used only
when this device's Vulkan driver can run it: on a PC whose driver reports
Vulkan 1.3, the setup installs the latest DXVK automatically (it is optional:
if that download fails, Windows is still prepared and the program starts with
wined3d, and **A** does not offer it again until JukaHub restarts). The Smart Pro's
driver is Vulkan 1.1, which DXVK 2 cannot use; the third row offers **DXVK
1.10.3** as an experiment (press **A** twice).

## Install the pieces manually

* **Runtime pack**: always installed by JukaHub itself (the setup row or
  Emulator updates), which records where it came from in
  `data/windows-runtime/pack/.jukahub-pack.json`; a hand-copied pack is not
  adopted and is replaced by the setup row. To run your own Box64 instead, set
  `JUKAHUB_BOX64`.
* **Your own Wine or Box64**: set `JUKAHUB_WINE` to a `wine` binary or script,
  and `JUKAHUB_BOX64` to a `box64` binary. An x86-64 Wine on an ARM device is
  started through Box64 automatically.
* **WestonPack**: copy `weston_pkg_0.2.squashfs` (PortMaster's name) to
  `<PortMaster>/libs/`, or `weston_pkg_0.2.aarch64.squashfs` to
  `data/windows-runtime/`; or set `JUKAHUB_WESTON_IMAGE` to the image, or
  `JUKAHUB_WESTON` to an unpacked `westonwrap.sh`. JukaHub's own copy is
  preferred over PortMaster's, and unpacked copies in other folders come last.
* **DXVK**: put a DXVK release's `x64`/`x32` folders in
  `data/windows-runtime/dxvk/`. It is enabled only when its version can run on
  this Vulkan (1.x needs 1.1, 2.x needs 1.3); set `JUKAHUB_DXVK` to its folder
  to force it on. Delete the folder to go back to wined3d.

Environment overrides:

| Variable | Effect |
|---|---|
| `JUKAHUB_WINEPREFIX` | Use this Wine prefix for every launch. |
| `JUKAHUB_WINE_PREFIX_IMAGE_MB` | Size of the card image when one is needed (512–3500). |
| `JUKAHUB_WINE` / `JUKAHUB_BOX64` | Use this Wine / Box64 instead of JukaHub's. |
| `JUKAHUB_WINE_ALLOW_NEWER=1` | Accept a runtime pack that names Wine 11 or newer. |
| `JUKAHUB_WINE_FLAVOR=hangover` | Install Hangover instead of Box64 + Wine (needs glibc 2.36; refused on the Smart Pro). `JUKAHUB_WINE_RUNTIME_URL` points at a specific archive (`https://` only). |
| `JUKAHUB_WESTON_IMAGE` / `JUKAHUB_WESTON` | Use this WestonPack image / unpacked `westonwrap.sh`. |
| `JUKAHUB_DXVK` / `JUKAHUB_VULKAN` | Force this DXVK folder on / name the Vulkan loader. |
| `JUKAHUB_WINE_DISPLAY` | `auto`, `x11`, `wayland`, `weston`, `weston-gl` or `headless` for every launch. |

A game profile's own `WINEDLLOVERRIDES` replaces JukaHub's default
(`mscoree,mshtml=;winemenubuilder.exe=d`, which avoids Wine's Mono/Gecko
download prompt); set it to re-enable a Mono or Gecko you installed yourself.
The first-run setup logs to `data/windows-runtime/logs/wineboot.log`.

## Game profiles

Wine game profiles start the same way as Windows Apps: the same runtime,
display, pad and clean-up. A profile's `env` wins over the runtime's defaults,
and its `display` field picks the display:

```json
{
  "schemaVersion": 1,
  "id": "notepad",
  "title": "Notepad",
  "runtime": "wine",
  "dir": "Roms/PC/Notepad",
  "executable": "notepad.exe",
  "display": "weston"
}
```

`auto` (the default) picks a desktop's X11 or Wayland, else WestonPack's
Windows (GDI) mode; `weston` and `weston-gl` ask for WestonPack's Windows (GDI)
and OpenGL modes; `x11`, `wayland` and `headless` are as before. On Windows the
program runs directly whatever `display` says. See
[GAME_PROFILES.md](GAME_PROFILES.md).

## Limits

* Nothing here has been checked on a TrimUI yet; see the note at the top.
* One Windows program at a time. A launcher that starts the real program and
  exits ends the session with it — point the app or profile at the program's
  own `.exe`.
* The display and the pad are lent for the whole session; while a Windows
  program runs, JukaHub's own screen is parked like during any game.
* No on-screen keyboard inside Windows programs.
* Only regular files the scan found are started; nothing is run from an
  arbitrary path or from the network.
