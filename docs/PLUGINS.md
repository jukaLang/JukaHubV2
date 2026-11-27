# Plugins: authoring and execution

Plugins are small programs invoked by buttons in a JukaHub configuration. Each
plugin needs a directory containing a `plugin.json` manifest and an entry script.
The runner captures the script's output and shows it as a toast. It does **not**
automatically create buttons for the plugin or let scripts draw a UI.

## Installing a plugin from the card

**Community → Plugin catalog** lists plugins you have placed in
`<data>/plugin-catalog` (on Windows, `%APPDATA%\JukaHub\plugin-catalog`). Each
folder needs a `plugin.json` manifest and the entry script it names. **A**
copies the highlighted folder into `plugins/`; it never overwrites a plugin
that is already installed, and a manifest with a problem is listed with the
reason instead of being hidden. Files over 2 MB, folders over 16 MB and
symlinks are refused. Nothing is fetched from the network: this is a way to
install a plugin someone handed you on a card, not an automatic store.

> **Trust first:** plugins run with JukaHub's permissions, inherit its
> environment and can read or modify accessible files or use the network. They
> are not sandboxed or automatically verified. Install only code you trust.
> Custom variables are also passed on the command line and may be visible to
> other processes; do not put secrets in them or print them in plugin output.

## Video sites from plugins

Downloads already accept any `http` or `https` video link that yt-dlp can read;
nothing is YouTube-only. A plugin can advertise the other sites it supports by
adding `"videoHosts"` to its manifest:

```json
{
  "videoHosts": ["vimeo.com", "dailymotion.com", "*.example.org"]
}
```

Each entry is a host name only — no scheme, port, path or credentials — and a
leading `*.` covers subdomains. Up to 16 hosts per plugin are read; a bad entry
is reported on the plugin's card and skipped. Switched-on plugins' hosts appear
on the Plugins card, in **Settings → Media & playback → Video sites from
plugins**, and are used to label downloads that come from those sites. This is
a label, not a permission: JukaHub never passes the host list to yt-dlp as an
argument, and no plugin can add yt-dlp command-line options through it.

## What switching a plugin on does

Settings → **Plugins** lists every plugin folder with an on/off switch (X).
A plugin that is on is part of JukaHub, not just a row on that page:

- **Switching it on answers.** The manifest's `onEnable` action runs at once
  and its first line is the toast ("System monitor is on: RAM 412M free · …").
  Switching it off says what went with it (its theme, ROM formats, Start menu
  buttons).
- **Its buttons are in the Start menu**, in a **Plugins** group, so they work
  from any screen (and from Home search). `quick` names which actions; with
  none named, the first action is listed. **Manage plugins** at the end of the
  group opens the Plugins page.
- **It can run at start-up.** `onStartup` runs an action when JukaHub starts.
  `"startupDaily": true` limits that to the first start of the day — but a
  handheld without a clock battery boots at the same date until network time
  arrives, so do not rely on it there. `"startupQuiet": true` keeps the
  start-up result off the toasts unless it is an error (Save guard backs up
  quietly at every start this way).
- **JukaHub can do the work.** Action ids that start with `@` are done by
  JukaHub itself with the plugin's own contributions, no script involved:

  | Action id | What JukaHub does |
  |-----------|-------------------|
  | `@apply-theme` | Switches to the plugin's (first) theme. |
  | `@restore-theme` | Goes back to the theme in use before `@apply-theme`. |
  | `@apply-pack` | Applies the plugin's (first) experience pack. |
  | `@undo-pack` | Takes the last pack switch back. |
  | `@use-profile` | Switches to the plugin's (first) control profile. |
  | `@default-profile` | Back to the Default controls. |
  | `@open-library` | Opens the game library. |
  | `@random-game` | Opens the game library, rolls one game from all of it and lands on it (A plays it, R rolls again). |
  | `@open-saves` | Opens the save manager (snapshots, restore). |

```json
{
  "actions": [
    {"id": "about", "label": "About the theme"},
    {"id": "@apply-theme", "label": "Use it now"}
  ],
  "onEnable": "about",
  "quick": ["@apply-theme"],
  "onStartup": "about",
  "startupQuiet": true
}
```

All of these keys are optional; a hook that names an action the plugin does
not have is ignored.

## The plugins that ship with JukaHub

They are copied into `plugins/` on the first scan and are on by default.

| Plugin | Buttons | What it does |
|--------|---------|--------------|
| **Save guard** (`saveguard`) | Back up saves now, Last backup, Restore a backup | Snapshots every emulator save folder through the save manager each time JukaHub starts (quietly), but only when a save actually changed — files are compared byte for byte, not by date. Keeps its own last five snapshots; the save manager keeps ten in all, so it can still push out manual snapshots past five. |
| **Library doctor** (`librarydoctor`) | Check my games, Remove junk files, Set duplicates aside | Finds the same game twice in one console folder, zips that do not open, empty files, and Mac/Windows litter (`._` twins, `.DS_Store`, `Thumbs.db`, `desktop.ini`, `__MACOSX`). Remove junk files deletes only that litter. Set duplicates aside compares copies whole and moves the extra ones to `Roms/.duplicates/` (the copy with saves stays); it never deletes a game, never touches BIOS files, and leaves disc sets (anything beside a `.cue` or `.m3u`) and copies in two different consoles alone. |
| **System monitor** (`sysinfo`) | Overview, Battery, Temperature and uptime | Free memory and card space with totals, battery with the time left, chip temperature and how long the device has been on. |
| **Random game** (`gamepick`) | Pick a game for me, Count games, Open the library | Rolls one game from the whole library and lands on it in the Games library. |
| **Quick notes** (`notes`) | Add note, All notes, Newest note, Clear notes | A pad in `notes.txt` in the plugin's folder; Add note opens the keyboard, Clear notes asks for a second press. |
| **Pocket Classic pack** (`classicpack`) | What it changes, Apply it now, Undo | An experience pack: Game Boy palette, hero-tile Home, Gaming controls, Arcade Corner. |
| **Neon Grid theme** (`neontheme`) | About the theme, Use it now, Back to my theme | A theme preset. |
| **Pad profiles** (`padprofiles`) | What it maps, Use Couch remote, Use default controls | A control profile for playing docked on a TV. |
| **Retro formats** (`retroformats`) | Formats it adds, Open the library | Teaches the library Pokémon mini, Supervision, Vectrex, Lynx and Arduboy files. WASM-4 is a built-in console now ([WASM-4](WASM4.md)). |

**They work everywhere.** Each also exists in Go (`player/plugnative.go`,
`player/plugnative_tools.go`), so they run on Windows, which has no `sh`, and
without starting a shell on the handheld. The Go version is used while the
plugin's script is the one JukaHub shipped (or cannot run here); edit the
script and, wherever `sh` exists, your script runs instead. A copy installed by
an older JukaHub is upgraded in place when its `version` is older, keeping
files you added (such as `notes.txt`).

**Retired plugins.** Hello, Dice, Daily quote, Clock, World clock and Handheld
tips no longer ship. A copy an older JukaHub installed is moved to
`plugins/.retired/` once (nothing is deleted); move a folder back into
`plugins/` to keep using it — on Windows it then needs `sh`, as their Go
versions are gone.

## Write your own

A plugin is a folder in `plugins/` with a `plugin.json` and an entry script. A
loose script directly in `plugins/` is not discovered. This one shows the free
space on the card:

```text
Apps/JukaHub/                 # or the desktop player's working directory
└── plugins/
    └── cardspace/            # the folder name is the plugin's id
        ├── plugin.json
        └── cardspace.sh
```

```json
{
  "name": "cardspace",
  "title": "Card space",
  "version": "1.0.0",
  "description": "Free space on the SD card.",
  "author": "Me",
  "entry": "cardspace.sh",
  "icon": "disk",
  "enabled": true,
  "actions": [{"id": "free", "label": "Free space"}],
  "onEnable": "free"
}
```

```sh
#!/bin/sh
case "$1" in
    free) df -h /mnt/SDCARD 2>/dev/null | awk 'NR==2 {print $4 " free of " $2}' ;;
    *) echo "Unknown action: $1" >&2; exit 1 ;;
esac
```

Press Refresh on the Plugins page (or reopen it), switch the plugin on, and its
Free space button is on the page and in the Start menu. The first line a script
prints is the toast; the Plugins page shows the rest. A shell interpreter named
`sh` must be on PATH (the handheld has one; on Windows install Git for Windows).

A config button can run any action too:

```json
{
  "type": "button", "text": "Card space",
  "x": 300, "y": 550, "width": "220", "height": "45",
  "trigger": "plugin_action", "triggerTarget": "cardspace", "triggerValue": "free"
}
```

To check the script from a terminal, run it from its own folder
(`sh cardspace.sh free`). Custom variables arrive as `--name=value` arguments
(see the action contract below). The bundled scripts in
[`player/plugins/`](../player/plugins) are plain POSIX `sh` and make good
starting points.

## Scaffold a plugin

`JukaHub --new-plugin <name>` writes the smallest plugin that loads:

```text
plugins/<name>/
├── plugin.json   # schemaVersion 1, one action, no permissions
├── main.sh       # answers that action
└── README.md
```

It refuses a name that is not lower-case letters, digits, `-` or `_` (at most
32 characters) and never writes into a folder that already exists. Run it from
the folder that holds `plugins/`, or name another folder with
`--plugins-dir <folder>`. It does not need a display, so it works over SSH on
the handheld. The result is the same shape as
[`examples/hello-plugin/`](../examples/hello-plugin).

## Manifest and discovery

Each immediate subdirectory of `plugins/` is scanned for a readable, valid
`plugin.json` with `"enabled": true`:

```json
{
  "schemaVersion": 1,
  "name": "cardspace",
  "version": "1.0.0",
  "description": "Free space on the SD card.",
  "author": "Me",
  "entry": "cardspace.sh",
  "enabled": true,
  "permissions": []
}
```

| Field | Meaning |
|-------|---------|
| `schemaVersion` | Manifest schema. Missing means 1; a newer number is listed as a problem instead of being half-read. |
| `name` | Ignored as the identifier: the containing directory name wins. |
| `version`, `author` | Strings displayed in the plugin list. |
| `permissions` | What the script needs beyond its own folder: `network`, `files`, `programs`, `settings`. Switching on a plugin that asks for a new one opens an Allow card; an empty list is the right starting point. |
| `description` | Free-text description. |
| `entry` | Path to a regular file, relative to the plugin directory; use `.sh` or `.py`. |
| `enabled` | Must be `true`; missing or false manifests are skipped. |
| `config` | Optional object; parsed but **not** passed to the script. |
| `title`, `icon` | Name and glyph on the Plugins page and in the Start menu. |
| `actions` | Buttons: `[{"id": "now", "label": "Time"}]`; ids starting with `@` are JukaHub's own (above). |
| `romParsers` | ROM formats for the game library: `[{"name": "Pokémon mini", "system": "pokemini", "extensions": [".min"]}]`. |
| `onEnable`, `onStartup`, `startupDaily`, `startupQuiet`, `quick` | What switching the plugin on does (above). |

`plugin.json` is unrelated to the package `manifest.json` described in
[Packages](PACKAGES.md). The root `plugins/` directory is relative to the
process working directory; the TrimUI launcher changes to `Apps/JukaHub/`.

The `plugin_refresh` trigger scans and updates `plugin_list`. A scan also occurs
when entering a scene with a `dynamiclist` whose `variable` is `plugin_list`;
the shipped Plugins scene uses a `textlog`, so **press Refresh there** rather
than relying on entering the scene.

`--new-plugin` writes a manifest at the current schema. A manifest without
`schemaVersion` stays readable as schema 1; a manifest from a newer JukaHub
(say `"schemaVersion": 2`) is shown with "uses schemaVersion 2; this JukaHub
reads up to 1" and is not loaded, rather than being partly understood.
Every scan rebuilds the catalog and live plugin map. After disabling, deleting
or fixing a manifest, press **Refresh** to update the list and loaded
contributions; safe mode skips plugin loading for that launch.

### ROM formats

`romParsers` lets a plugin teach the game library a cartridge format it does not
know. Each entry names the format, gives a short lower-case `system` id and the
extensions (with or without the leading dot). Files then appear in LIBRARY under
the format's name, collected as **Plugins**, whether they sit in a folder named
after `system` or the display name (spaces and punctuation ignored) or use the
extension inside a known console folder. Extensions and folder names the
built-in catalogue already owns stay with the built-in system, so a parser
cannot claim `.nes` from NES. A plugin's own subfolder under any ROM root works
for `.zip` sets too.

```json
"romParsers": [
  {"name": "Pokémon mini", "system": "pokemini", "extensions": [".min"]}
]
```

## Action contract

A button with `trigger: "plugin_action"` uses `triggerTarget` as the plugin
folder and `triggerValue` as the action. It invokes the entry synchronously:

```text
sh <entry-path> <action> --key=value ...
# or
python3 <entry-path> <action> --key=value ...
```

- Extensions are case-sensitive: `.sh` runs with `sh`, `.py` with `python3`.
  Other extensions return a status message **without executing the file**.
  Stock TrimUI does not ship Python, so use POSIX `sh` unless you have supplied
  Python yourself. The script does not need an executable bit.
- The action is the first argument; each remaining argument is one custom
  runtime variable formatted as `--name=value`. Argument order is unspecified.
  Values are formatted with Go's `%v`, not JSON; quote `"$@"` when parsing and
  do not rely on typed or structured round-tripping. The runner does not invoke
  a shell to expand these arguments.
- Only the `Variables.Custom` snapshot is passed. Built-in typed settings such
  as `tspUsername` are not automatically passed as arguments or environment
  variables. In config files, custom keys such as `plugin_name` can be placed
  directly in `variables`; see [Configuration](CONFIGURATION.md).
- The script runs in the entry file's directory and inherits the app's
  environment. Standard output and standard error are combined. A nonzero exit
  or missing interpreter returns `Plugin error: ...` with captured output; the
  UI displays the returned text as an informational toast. Keep output short.
- The script blocks the UI and has no timeout. Keep actions quick and avoid
  interactive input. It cannot directly call `PluginContext`, change scenes,
  render, or write values back into the running app.

The runner rejects entry paths that **lexically** escape the plugin folder and
requires an existing regular file. This is not a sandbox: symlinks are not
resolved by the containment check, and scripts themselves can access anything
JukaHub can. Avoid symlinks and untrusted plugins.

## Troubleshooting

| Symptom | Check |
|---------|-------|
| Plugin is missing from the list | Check the folder layout, valid JSON, `"enabled": true`, the working directory and safe mode; then press Refresh. |
| Disabled or removed plugin still appears | Restart JukaHub, then refresh: a normal scan retains earlier entries. |
| `Plugin "cardspace" not found` | Press Refresh first and check `triggerTarget` matches the folder name exactly. |
| `Plugin entry ... not found` or unsafe entry path | Check `entry` names an existing file inside the plugin folder. |
| `Plugin error` | Check the action, script output and that `sh` or `python3` is on PATH; run the script from a terminal. |
| A message says an action executed but nothing happened | Check that the entry extension is lowercase `.sh` or `.py`; other extensions are not run. |
| UI freezes | The script is still running; there is no timeout. Shorten the action and restart if necessary. |

If a plugin prevents normal use, start with `--safe-mode`, set
`JUKAHUB_SAFE_MODE=1`, or create an empty `safe-mode.flag` in the app working
directory. Safe mode skips plugin loading, updates, tool installation and
background network work without editing the configuration. Remove the flag or
other safe-mode setting and restart when the plugin has been fixed or removed.

## Internal Go API

`LoadPlugins`, `GetPlugins` and `ExecutePluginAction` in `player/plugins.go`
back the UI path. `InstallPlugin` copies **one file** into a new plugin folder;
it does not create a manifest or install a complete directory. `UninstallPlugin`
removes a directory and does not validate the name itself; callers must validate
names before invoking it. Neither install nor uninstall is exposed as an
on-device plugin-management trigger. Manual copying of trusted plugin folders
is the supported workflow described above.

## Related

- [Configuration reference](CONFIGURATION.md) — elements, triggers and variables
- [Packages](PACKAGES.md) — the separate package format
- [Security](../SECURITY.md) — JukaHub's security model
