# Windows apps

**Windows Apps** is a small launcher for basic Microsoft Windows programs on
the ARM64 handheld build. It discovers `.exe` files, launches them through
Wine, and bridges the display gap on TrimUI devices that have SDL2 but no X11.

Open it from **Start → Windows Apps**, or from the pinned **Windows Apps** row
in **Emulators**.

## What it can run

The target is simple Win32 GUI programs and console tools — Notepad, small
utilities, older 2D apps. A Windows program does not become ARM-native: Wine
provides the Windows APIs, and Box64 or Hangover's emulator DLLs provide the
x86/x86-64 instruction translation.

JukaHub installs what a launch needs by itself: press **A** on an app and, if
the runtime is missing, the setup page opens on the missing piece with a line
naming it and its download. One more **A** installs the whole chain — Wine,
then the display bridge, then DXVK when a Vulkan loader makes it usable — and
the app starts when the last piece is in. The download never begins without
that confirming press. See [Automatic setup](#automatic-setup).

What it does **not** provide:

* a Vulkan 1.3 driver. DXVK is installed only when the firmware already has a
  Vulkan loader, and it cannot make one appear;
* Proton, VKD3D, or a guarantee that a particular app works. Test app by app
  and use the log.

## The display problem

Wine does not render directly to KMS/DRM or a framebuffer. `winex11.drv` needs
an X server and `winewayland.drv` needs a Wayland compositor. A stock TrimUI
Smart Pro has neither, so `wine app.exe` has no window to draw into.

JukaHub handles this with a display bridge, selected in this order:

1. `WAYLAND_DISPLAY` — launch Wine directly in the existing Wayland session.
2. `DISPLAY` — launch Wine directly through X11.
3. WestonPack's `westonwrap.sh` — start a minimal Weston/Xwayland session on
   DRM/KMS and launch Wine inside it.
4. `display: "headless"` — only for console programs; there is no visible
   window.

If none of the first three is available, the app stays listed but the row says
**Unavailable** and explains what is missing. A missing display is reported
before Wine is started, rather than treated as a successful silent launch.

## Automatic setup

Open **Start → Windows Apps** and press **A** on an app. If it cannot start
yet, the launcher does not dead-end: the setup page opens on the missing row,
with a notice saying which pieces have to be installed for that app, and the
app is remembered as the pending launch. Press **A** once more and JukaHub
installs the missing chain in order:

1. **Wine** (Hangover ARM64), when no Wine was found;
2. **WestonPack**, when there is no X11 or Wayland display to draw into;
3. **DXVK**, when a Vulkan loader exists and DXVK is not installed yet.

Each step is re-checked before it starts, so a piece that is already present is
not downloaded again. When the last step succeeds the pending app launches by
itself; if one fails, the installer line shows why and nothing is launched.
Closing the panel cancels the pending launch — the runtime install keeps
going, but the app will not open later on its own.

## Install from the setup page

Open **Start → Windows Apps**, then press **X** (or tap **Setup**). The setup
page has one install action per runtime piece:

| Row | What it does |
|-----|--------------|
| **Install Wine (Hangover ARM64)** | Downloads the current ARM64 Hangover release, unpacks its Debian packages into `data/windows-runtime/hangover`, writes `data/windows-runtime/bin/wine`, runs `wine --version`, and reports the result. |
| **Install display runtime (WestonPack)** | Downloads `weston_pkg_0.2.aarch64.squashfs` from PortMaster and extracts it with `unsquashfs` when that tool exists. |
| **Install DXVK (Direct3D over Vulkan)** | Downloads the latest DXVK release and copies its `x64`/`x32` trees into `data/windows-runtime/dxvk`. Refused when no Vulkan loader is present, because DXVK cannot start without one. |

Select a row with Up/Down and press **A**, or tap it. The card keeps showing
the current step (`Installer: downloading …`) while the download runs in the
background, so the UI stays responsive. Wine is Linux-only: on the Windows
desktop build pressing an install row explains that Windows runs the `.exe`
directly.

The Wine install resolves the latest release through the GitHub API and picks
the Debian 12 ARM64 archive (falling back to another ARM64 `.tar`). The
separate `dlls.tar` asset is deliberately skipped. To install a specific build
instead, set `JUKAHUB_WINE_RUNTIME_URL` to a `.tar`, `.tar.gz` or `.zip` of an
ARM64 Wine tree before launching.

Hangover publishes that archive as a set of Debian `.deb` packages, not as an
installed tree. JukaHub unpacks their `data.tar.xz` payloads itself, so neither
`dpkg` nor root access is needed. The download is roughly 300 MB, the unpacked
runtime is about 2.5 GB, and the download and package copies coexist briefly,
so leave at least 4 GB free in the data folder.

Unpacking is CPU-bound and can take several minutes on the handheld; the setup
page keeps showing `Installer: unpacking …` while it runs.

Nothing outside `data/windows-runtime` is modified. The extracted `wine`
launcher is ARM64; x86/x86-64 apps still go through Hangover's Box64/FEX
dependencies, which the same packages provide.

The installer cannot make a newer glibc appear. Hangover's published ARM64
builds target Debian/Ubuntu releases, and a stock TrimUI firmware may refuse
the binary at load time. When that happens the setup page reports the exact
`wine --version` error; use `JUKAHUB_WINE_RUNTIME_URL` with a build matching
your firmware, or the PortMaster build described below.

### Display runtime caveats

WestonPack ships as SquashFS. JukaHub extracts it userland when `unsquashfs`
is present; otherwise it keeps the image at
`data/windows-runtime/weston_pkg_0.2.aarch64.squashfs` and tells you to let
PortMaster install the runtime. The setup page shows **WestonPack** as
missing until a `westonwrap.sh` is found.

## Install the pieces manually

Hangover and WestonPack are separate projects with their own licenses and
release packages. Their binaries depend on the firmware's glibc and graphics
stack, and neither project publishes a package specifically tested on the
original Smart Pro, so this remains the experimental part of the feature.

### Wine on ARM64

Use a build that matches the device's ARM64 Linux userland. [Hangover][hangover]
runs Win32/Win64 programs on ARM64 Wine using Box64/FEX emulator DLLs; it is
the most direct match for "Windows apps on an ARM handheld". Its published
packages target Debian/Ubuntu releases, so a stock TrimUI firmware with an
older glibc may need a matching build. A PortMaster Wine build under
`/mnt/SDCARD/PortMaster/wine` is also detected.

Put the binary in one of these places:

```text
data/windows-runtime/bin/wine
data/windows-runtime/wine
/mnt/SDCARD/Apps/Wine/bin/wine
/mnt/SDCARD/PortMaster/wine/bin/wine
```

or set `JUKAHUB_WINE` to the path. If the Wine ELF is x86-64, also install
Box64; JukaHub prefixes it automatically.

### A display on TrimUI

Install [WestonPack][westonpack] through PortMaster. It provides
`westonwrap.sh` and the X11/Wayland/GL compatibility libraries. Its published
compatibility table does not list the original Smart Pro, so test it on your
firmware before relying on it. Put `westonwrap.sh` at:

```text
data/windows-runtime/westonwrap.sh
```

or point `JUKAHUB_WESTON` at it. JukaHub also checks the usual PortMaster
runtime folders and `/tmp/weston/westonwrap.sh`.

The launch uses WestonPack's on-screen DRM mode:

```text
westonwrap.sh drm gl kiosk system WINEPREFIX=… wine app.exe
```

WestonPack labels Wine support **experimental**. If the screen stays black,
check `data/windows-runtime/logs/` and the WestonPack/PortMaster logs. If the
compositor cannot acquire DRM while JukaHub is running, quit JukaHub before
launching the app.

### Direct3D (DXVK)

Wine's built-in D3D path is enough for simple Win32 windows. Direct3D games
need a translation layer plus a Vulkan driver. JukaHub's installer puts it here
when a Vulkan loader exists (or install a build by hand):

```text
data/windows-runtime/dxvk/x64/d3d11.dll
data/windows-runtime/dxvk/x32/d3d11.dll   (32-bit prefixes only)
```

or set `JUKAHUB_DXVK` to the DXVK directory (or directly to `d3d11.dll`).
JukaHub also checks `/mnt/SDCARD/Apps/DXVK`, the PortMaster runtime folders and
`/mnt/SDCARD/PortMaster/wine/dxvk`.

DXVK is only installed and enabled when a Vulkan loader is present
(`libvulkan.so.1`, or set `JUKAHUB_VULKAN`). When both are present JukaHub adds:

```text
WINEDLLOVERRIDES=dxgi,d3d9,d3d10core,d3d11,d3d12=n,b
```

An existing `WINEDLLOVERRIDES` value is never replaced — a profile or the user
already chose its DLL order. The Windows Apps setup page (X) shows whether DXVK
and Vulkan were detected. This still requires a Vulkan 1.3-capable driver from
the firmware/PortMaster; JukaHub cannot provide one, so on a device without a
Vulkan loader the DXVK row says **needs Vulkan** and the automatic chain skips
it.

### App folders

`.exe` files are discovered up to four folders deep in:

```text
data/windows-apps/
/mnt/SDCARD/Roms/PC/
/mnt/SDCARD/Apps/Windows/
/mnt/SDCARD/Games/Windows/
```

Wine's own `windows/`, `system32/` and `syswow64/` folders are skipped.

## Controls

| Input | Action |
|-------|--------|
| D-pad Up / Down | Move through apps |
| A / Enter / Space | Launch the selected app |
| X / `x` / `h` | Show the setup page |
| Setup: Up / Down | Choose Wine or the display runtime |
| Setup: A / Enter | Install the selected runtime (after a needs-runtime launch: install everything missing, then start the app) |
| Y / `y` / `r` | Rescan the app folders |
| B / Esc / Backspace | Close setup, then the launcher |
| Mouse / touch | Tap a row to launch; tap outside to close |

When an app is running the row says **Running**. JukaHub stays responsive and
reaps the process in the background; its stdout/stderr go to
`data/windows-runtime/logs/<title>-<hash>.log`.

## Game profiles

The existing Game Profiles format also supports Wine. Add an optional
`"display"` field when a profile needs an override:

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

Accepted values are `auto` (default), `x11`, `wayland`, `weston` and
`headless`. The same display check applies to Game Profiles, so a Wine profile
without a usable display is shown as **Unavailable** with the reason.

You can set a default for all Wine launches with
`JUKAHUB_WINE_DISPLAY=auto|weston|x11|wayland|headless`.

## Limitations

* No physical TrimUI test was possible in this change; WestonPack's DRM output,
  DXVK and a particular Wine build still need to be checked on the device.
* WestonPack takes over the screen. Close other full-screen apps if the
  compositor cannot acquire DRM.
* A shared Wine prefix (`data/windows-runtime/prefix`) keeps the first-run
  setup cost down. Apps with conflicting DLL overrides may need their own
  `WINEPREFIX`.
* Controller remapping is not per app. Game Profiles' `controller` field is
  informational only.
* The launcher only accepts regular `.exe` files from the folders above; it
  does not execute arbitrary paths or import profiles from the network.
* DXVK is only installed when a Vulkan loader is present, and its 32-bit and
  64-bit DLLs must match the Wine prefix. The automatic chain installs it once;
  reinstalling from the setup row refreshes it.

[hangover]: https://github.com/AndreRH/hangover
[westonpack]: https://github.com/binarycounter/Westonpack
