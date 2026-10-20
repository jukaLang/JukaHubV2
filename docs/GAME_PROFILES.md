# Game profiles

Game profiles are small JSON files that tell JukaHub how to start a game. They
are aimed at Windows games running through **Box64** or **Wine** on the TrimUI,
and also support native binaries and shell scripts.

Open the launcher from **Start → Game Profiles** or from the pinned
**Game Profiles** row at the top of **Emulators**.

| Input | Action |
|-------|--------|
| D-pad Up / Down | Move through profiles |
| A / Enter / Space | Launch the selected profile |
| X / `x` | Export the selected profile to `export/<id>.json` |
| Y / `y` / `r` | Rescan `profiles.json`, `import/` and `local/` |
| B / Esc / Backspace | Back (returns to Emulators when opened there) |
| Mouse / touch | Tap a row to launch it; tap outside to close |

The panel shows **Ready**, **Running**, or **Unavailable** for each row. When a
profile is unavailable, the second line gives the exact reason (missing folder,
missing executable, Wine/Box64 not found, or a path outside the allowed roots).

## Folder layout

Profiles live in JukaHub's data folder, next to the application:

```text
data/game-profiles/
├── profiles.json      shared catalog
├── import/            community profiles, loaded automatically
├── local/             your overrides; a matching id wins
├── export/            files written by X in the panel
├── logs/              one log per launched game
├── games/             optional portable game folders
└── wineprefix/        created on the first Wine launch
```

On a TrimUI this is `Apps/JukaHub/data/game-profiles`. The folder is reachable
over the built-in FTP/SFTP server, so copying a profile to `import/` and
pressing **Y** is enough to load it.

## Format

Every file needs `schemaVersion: 1`. A file can contain one profile:

```json
{
  "schemaVersion": 1,
  "id": "nfsu2",
  "title": "Need for Speed: Underground 2",
  "runtime": "wine",
  "dir": "Roms/PC/NFS Underground 2",
  "executable": "speed.exe",
  "args": ["-windowed"],
  "env": {"WINEDLLOVERRIDES": "d3d9=n,b"},
  "controller": "xinput",
  "testedFirmware": "TrimUI Smart Pro 1.1.0",
  "notes": "Runs at 30 fps with the widescreen fix."
}
```

…or a bundle for a community pack:

```json
{
  "schemaVersion": 1,
  "profiles": [
    {
      "schemaVersion": 1,
      "id": "game-one",
      "title": "Game One",
      "runtime": "box64",
      "dir": "Games/Game One",
      "executable": "game.exe"
    },
    {
      "schemaVersion": 1,
      "id": "game-two",
      "title": "Game Two",
      "runtime": "wine",
      "dir": "Games/Game Two",
      "executable": "game.exe"
    }
  ]
}
```

A malformed entry in a bundle is skipped and reported; the valid entries still
load. A malformed single-profile file is skipped as a whole.

| Field | Required | Meaning |
|-------|----------|---------|
| `schemaVersion` | yes | Must be `1` for this build. |
| `id` | yes | Short lowercase identifier: letters, digits, `.`, `-`, `_`; max 64 characters. Also names the export and log files. |
| `title` | no | Display name; defaults to `id`; max 120 characters. |
| `runtime` | yes | `native`, `box64`, `wine`, or `script`. |
| `dir` | yes | Game folder. Relative paths are searched under the game roots. Absolute paths must already be inside a root. |
| `executable` | yes | File inside `dir`; must stay inside it after symlinks are resolved. |
| `args` | no | Up to 128 arguments passed directly to the program (no shell parsing). |
| `env` | no | Up to 64 environment variables for the game process. |
| `display` | no | Wine display backend: `auto` (default), `x11`, `wayland`, `weston` or `headless`. |
| `controller` | no | Informational label such as `xinput` or `keyboard`. |
| `testedFirmware` | no | Firmware/device combination you tested, shown in the panel. |
| `notes` | no | Free text shown in the panel; max 2000 characters. |

### Runtimes

| Runtime | What JukaHub runs | Executable check |
|---------|-------------------|------------------|
| `native` | The executable directly. | Any regular file. |
| `script` | `sh <executable> <args…>` | Must be `.sh`. |
| `box64` | `box64 <executable> <args…>` | `.sh`, `.bat`, `.cmd`, `.lnk` and `.url` are rejected. |
| `wine` | `wine <executable> <args…>`, or `box64 wine …` when the Wine ELF is x86. | `.exe`, `.bat`, `.cmd` or `.com`. |

If Wine is configured with `WINEPREFIX` in the profile's `env`, that prefix is
used. Otherwise JukaHub creates and uses
`data/game-profiles/wineprefix` and replaces any inherited prefix.

Wine still needs a display server. Profiles default to `display: "auto"`:
JukaHub uses an existing Wayland or X11 session, then falls back to
WestonPack's `westonwrap.sh` on DRM/KMS. On a stock TrimUI with no X11, install
WestonPack and set `JUKAHUB_WESTON`, or use `display: "headless"` for a console
program. See [WINDOWS_APPS.md](WINDOWS_APPS.md) for the full setup.

When DXVK is installed in `data/windows-runtime/dxvk` (or `JUKAHUB_DXVK`) and a
Vulkan loader is present, JukaHub adds the DXVK DLL overrides automatically. A
profile that already sets `WINEDLLOVERRIDES` keeps its own value. Direct3D games
also need a Vulkan 1.3-capable driver; the setup page in Windows Apps reports
what was detected.

### Game roots

Relative `dir` values are tried under these roots in order. The first existing
folder that stays inside its root wins:

* always: `data/game-profiles/games`
* TrimUI/Linux: `/mnt/SDCARD`
* Windows: the folder containing `JukaHub.exe`, `%USERPROFILE%\Games`, and
  `%USERPROFILE%\Saved Games`

Use a relative `dir` (for example `Roms/PC/My Game`) when sharing a profile.
Absolute paths work locally but are unlikely to match another device.

### Runtime discovery

JukaHub looks for Box64 in this order:

1. `JUKAHUB_BOX64` (must point at a regular file)
2. `/usr/trimui/bin/box64`
3. `/mnt/SDCARD/Apps/Box64/box64`
4. `/mnt/SDCARD/Apps/box64/box64`
5. `/mnt/SDCARD/PortMaster/box64`
6. `/mnt/SDCARD/Apps/PortMaster/box64`
7. `/mnt/SDCARD/Emus/PortMaster/box64`
8. `box64` on `PATH`

Wine uses the same pattern with `JUKAHUB_WINE`,
`data/windows-runtime/bin/wine`, `data/windows-runtime/wine`,
`/usr/trimui/bin/wine`, `/usr/trimui/bin/wine64`, the two `Apps/Wine` paths,
the PortMaster paths, then `wine`/`wine64` on `PATH`.

If the Wine binary is an x86 or x86-64 ELF, JukaHub automatically prefixes it
with Box64. Wrapper scripts and native ARM Wine binaries run directly.

## Import, export and overrides

* Drop a single profile or a bundle into `import/` and press **Y**. The file is
  read in place and never moved or edited.
* **X** writes the selected profile to `export/<id>.json`. Share that file, or
  copy it into another device's `import/` folder.
* `local/` is for your own edits. A profile in `local/` wins over the same `id`
  in `import/` or `profiles.json`.
* Duplicate IDs within the same layer are ignored and reported in the log.
* `profiles.json` is the shared database. It is created with an empty profile
  list on first launch; JukaHub never overwrites it.

## Logs

Each launch appends to `data/game-profiles/logs/<id>.log`: timestamp, command
path, argument count, working directory, and the child's stdout/stderr. The log
does not contain the profile's environment values, so a token in `env` is not
copied there.

JukaHub starts the game in its own process group and reaps it in the background.
It never waits for a game on the UI thread. Closing JukaHub does not terminate a
game that is already running.

## Security

A profile is executable content: it chooses a program and can set its
environment. Only import profiles from people you trust, and read the JSON before
you run it.

The path checks are deliberate:

* native, Box64 and Wine launches go straight to `exec` with an argument
  vector, so arguments cannot inject shell commands (`script` profiles
  explicitly run their `.sh` through `sh`, without interpolating the args);
* `executable` must be a regular file inside `dir`;
* `dir` must stay inside one of the game roots, including after symlinks are
  evaluated;
* `..` and absolute executable paths are rejected;
* `schemaVersion` is required and checked before any entry is offered.

Environment overrides are passed through to the game, so a hostile profile can
still set variables such as `LD_PRELOAD`. The sandbox is about accidental path
escapes, not a security boundary against a malicious executable. JukaHub runs as
the normal user and never elevates privileges for a game.

## Troubleshooting

| Panel message | Fix |
|---------------|-----|
| `game folder ... not found` | Check `dir` against the roots above, or use an absolute path inside a root. |
| `executable: ... no such file` | Check the file name and case inside the game folder. |
| `wine not found` | Install Wine, or set `JUKAHUB_WINE` to its binary. |
| `box64 not found` | Install Box64, or set `JUKAHUB_BOX64` to its binary. |
| `no display server` | Install WestonPack and set `JUKAHUB_WESTON`, or use `display: "headless"` for a console program. |
| `outside the allowed game folders` | Move the game under the SD card (or the data-folder `games/`) and use a relative `dir`. |
| `already running (pid …)` | Wait for the current instance to exit; the panel shows **Running** until it is reaped. |
| Game exits immediately | Read `data/game-profiles/logs/<id>.log`; Wine DLL overrides and missing prefixes are the usual causes. |

## Current limits

* There is no on-device profile editor yet; profiles are authored as JSON and
  copied over FTP/SFTP or the SD card.
* All Wine profiles share one prefix unless they set `WINEPREFIX` themselves.
* `controller` and `testedFirmware` are informational; JukaHub does not remap
  controller buttons per game.
* A profile starts one process. If a launcher script needs to set up a game
  tree, use a `script` profile and let the script handle the tree.
