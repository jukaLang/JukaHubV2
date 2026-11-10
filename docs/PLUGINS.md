# Plugins: authoring and execution

Plugins are small programs invoked by buttons in a JukaHub configuration. Each
plugin needs a directory containing a `plugin.json` manifest and an entry script.
The runner captures the script's output and shows it as a toast. It does **not**
automatically create buttons for the plugin or let scripts draw a UI.

> **Trust first:** plugins run with JukaHub's permissions, inherit its
> environment and can read or modify accessible files or use the network. They
> are not sandboxed or automatically verified. Install only code you trust.
> Custom variables are also passed on the command line and may be visible to
> other processes; do not put secrets in them or print them in plugin output.

## What switching a plugin on does

Settings → **Plugins** lists every plugin folder with an on/off switch (X).
A plugin that is on is part of JukaHub, not just a row on that page:

- **Switching it on answers.** The manifest's `onEnable` action runs at once
  and its first line is the toast ("Clock is on: Mon 09 Nov · 18:41").
  Switching it off says what went with it (its theme, ROM formats, Start menu
  buttons).
- **Its buttons are in the Start menu**, in a **Plugins** group, so they work
  from any screen (and from Home search). `quick` names which actions; with
  none named, the first action is listed. **Manage plugins** at the end of the
  group opens the Plugins page.
- **It can greet you at start-up.** `onStartup` runs an action when JukaHub
  starts; with `"startupDaily": true` only on the first start of the day (the
  Daily quote plugin does this).
- **Theme, pack and control-profile plugins can be used in one press.** Action
  ids that start with `@` are done by JukaHub itself with the plugin's own
  contributions, no script involved:

  | Action id | What JukaHub does |
  |-----------|-------------------|
  | `@apply-theme` | Switches to the plugin's (first) theme. |
  | `@restore-theme` | Goes back to the theme in use before `@apply-theme`. |
  | `@apply-pack` | Applies the plugin's (first) experience pack. |
  | `@undo-pack` | Takes the last pack switch back. |
  | `@use-profile` | Switches to the plugin's (first) control profile. |
  | `@default-profile` | Back to the Default controls. |
  | `@open-library` | Opens the game library. |

```json
{
  "actions": [
    {"id": "about", "label": "About the theme"},
    {"id": "@apply-theme", "label": "Use it now"}
  ],
  "onEnable": "about",
  "quick": ["@apply-theme"],
  "onStartup": "about",
  "startupDaily": true
}
```

All four keys are optional; a hook that names an action the plugin does not
have is ignored.

**The bundled plugins work everywhere.** The thirteen plugins that ship with
JukaHub also exist in Go (`player/plugnative.go`), so they run on Windows,
which has no `sh`, and without starting a shell on the handheld. The Go
version is used while the plugin's script is the one JukaHub shipped (or
cannot run here); edit the script and, wherever `sh` exists, your script runs
instead. A copy installed by an older JukaHub is upgraded in place when its
`version` is older, keeping files you added (such as the notes plugin's
`notes.txt`).

## Try the example plugin

The repository ships a few examples in [`player/plugins/`](../player/plugins).
This section walks through `hello`: [`player/plugins/hello/hello.sh`](../player/plugins/hello/hello.sh)
with its required [`plugin.json`](../player/plugins/hello/plugin.json). It prints
`Hello, friend!` for the `greet` action, or uses the optional custom variable
`plugin_name` for the greeting. It does not modify files or use the network.
No example is automatically included in release packages: copy the plugin's
folder (both files) to your installation as follows:

```text
Apps/JukaHub/                 # or the desktop player's working directory
└── plugins/
    └── hello/                # triggerTarget is this directory name
        ├── plugin.json
        └── hello.sh
```

1. Copy the example's `hello` folder to `plugins/` as shown. A loose script
   directly in `plugins/` is not discovered. A shell interpreter named `sh`
   must be available on PATH; on Windows this may require installing one.
2. In your `jukaconfig.json` (or the web builder), add the following buttons to
   an existing scene's `elements` array. Adjust the coordinates to leave room
   for existing elements, then save and reload the config or restart JukaHub:

   ```json
   {
     "type": "button", "text": "Refresh plugins",
     "x": 60, "y": 550, "width": "220", "height": "45",
     "trigger": "plugin_refresh"
   },
   {
     "type": "button", "text": "Say hello",
     "x": 300, "y": 550, "width": "220", "height": "45",
     "trigger": "plugin_action", "triggerTarget": "hello", "triggerValue": "greet"
   }
   ```

3. Press **Refresh plugins** before **Say hello**. The greeting appears as a
   toast. The shipped Plugins scene has its own Refresh button, but its list is
   informational: selecting an entry does not run an action.
4. Optionally add `"plugin_name": "Alex"` inside the top-level `variables`
   object of `jukaconfig.json`; after reloading, the greeting is `Hello, Alex!`.
   If the variable is missing or empty, the script uses `friend`.

To check the script independently from a terminal, run it from its own folder:

```sh
cd plugins/hello
sh hello.sh greet '--plugin_name=Alex'
# Hello, Alex!
sh hello.sh greet
# Hello, friend!
```

An unknown action prints an error and exits nonzero. This terminal check tests
the script, not discovery or the JukaHub button; use the refresh and action
buttons to check the complete path.

## The other examples

Every example is a folder with a manifest and one `sh` script, installed the
same way: copy the folder into `plugins/` and add a button whose `triggerTarget`
is the folder name.

| Example | Actions | What it does |
|---------|---------|--------------|
| [`clock`](../player/plugins/clock/clock.sh) | `now`, `date`, `both`, `uptime` | The time, the date, or how long the device has been up. Pass `--clock_24h=0` for a 12-hour clock. |
| [`dice`](../player/plugins/dice/dice.sh) | `roll`, `coin`, `pick` | A die roll (`--dice_sides=N`, 2 to 1000), a coin flip, or one entry picked from `--dice_list=a,b,c`. |
| [`notes`](../player/plugins/notes/notes.sh) | `add`, `last`, `count`, `clear` | A one-line-at-a-time scratchpad kept in `notes.txt` beside the script. `add` takes `--notes_text=...`. |
| [`sysinfo`](../player/plugins/sysinfo/sysinfo.sh) | `status`, `memory`, `disk`, `power` | Free memory, free disk space and battery level; anything the system does not report is said plainly instead of failing. |

`notes` is the only example that writes a file, and it writes inside its own
folder: deleting `notes.txt` empties the pad. `sysinfo` reads `/proc/meminfo` and
`/sys/class/power_supply`, so on a desktop it reports only what that system
exposes — `Memory info unavailable` there is a correct answer, not an error.

All of them print one short line, which is what a toast can show. The scripts are
plain POSIX `sh` with no network use; the only external commands are `date`,
`awk`, `cut`, `tail` and `wc`, in the actions that need them.

## Manifest and discovery

Each immediate subdirectory of `plugins/` is scanned for a readable, valid
`plugin.json` with `"enabled": true`:

```json
{
  "name": "hello",
  "version": "1.0.0",
  "description": "Example greeting plugin for JukaHub.",
  "author": "JukaHub",
  "entry": "hello.sh",
  "enabled": true
}
```

| Field | Meaning |
|-------|---------|
| `name` | Ignored as the identifier: the containing directory name wins. |
| `version`, `author` | Strings displayed in the plugin list. |
| `description` | Free-text description. |
| `entry` | Path to a regular file, relative to the plugin directory; use `.sh` or `.py`. |
| `enabled` | Must be `true`; missing or false manifests are skipped. |
| `config` | Optional object; parsed but **not** passed to the script. |
| `title`, `icon` | Name and glyph on the Plugins page and in the Start menu. |
| `actions` | Buttons: `[{"id": "now", "label": "Time"}]`; ids starting with `@` are JukaHub's own (above). |
| `onEnable`, `onStartup`, `startupDaily`, `quick` | What switching the plugin on does (above). |

`plugin.json` is unrelated to the package `manifest.json` described in
[Packages](PACKAGES.md). The root `plugins/` directory is relative to the
process working directory; the TrimUI launcher changes to `Apps/JukaHub/`.

The `plugin_refresh` trigger scans and updates `plugin_list`. A scan also occurs
when entering a scene with a `dynamiclist` whose `variable` is `plugin_list`;
the shipped Plugins scene uses a `textlog`, so **press Refresh there** rather
than relying on entering the scene. The current scan does not clear previously
loaded entries in normal mode: after disabling, deleting or breaking a manifest,
restart JukaHub and refresh to remove the stale entry. Safe mode clears the
loaded list for that launch.

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
| `Plugin "hello" not found` | Press Refresh first and check `triggerTarget` matches the folder name exactly. |
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
