# Plugin API reference

JukaHub loads plugins from a `plugins/` directory next to the application. A
plugin is **a directory with a `plugin.json` manifest and one script**; the
script is invoked with an action name and the current config variables as
arguments, and whatever it prints is shown on screen.

Plugins are ordinary programs: they run with the same permissions as JukaHub.
There is no sandbox beyond path confinement — install only plugins you trust.

---

## Directory layout

```text
Apps/JukaHub/
└── plugins/
    └── hello/                 ← the plugin name is the folder name
        ├── plugin.json        ← manifest (required)
        └── hello.sh           ← entry point, named by "entry"
```

The directory is created on first use. A subdirectory without a readable
`plugin.json` is skipped.

## `plugin.json`

```json
{
  "name": "hello",
  "version": "1.0.0",
  "description": "Says hello and echoes the variables it was given.",
  "author": "you",
  "entry": "hello.sh",
  "enabled": true,
  "config": {}
}
```

| Field | Type | Meaning |
|-------|------|---------|
| `name` | string | display name; **the directory name always wins** |
| `version` | string | shown in the plugin list |
| `description` | string | free text |
| `author` | string | shown in the plugin list |
| `entry` | string | script to run, relative to the plugin directory |
| `enabled` | bool | `false` skips the plugin during the scan |
| `config` | object | reserved for per-plugin settings; loaded but not passed to scripts yet |

A manifest that does not parse, or one with `"enabled": false`, is skipped.
Loading happens when the **Plugins** screen is entered and when the
`plugin_refresh` trigger runs — not at every launch.

> `plugin.json` is the manifest for *this* system only. It is unrelated to the
> package `manifest.json` described in [Packages](PACKAGES.md).

## Entry point and execution contract

When a config element fires the `plugin_action` trigger, JukaHub runs:

```bash
sh      <entry> <action> --key=value --key2=value2 …
# or
python3 <entry> <action> --key=value --key2=value2 …
```

| Aspect | Behaviour |
|--------|-----------|
| Interpreter | `sh` for a `.sh` entry, `python3` for a `.py` entry |
| `$1` | the action string from `triggerValue` |
| Remaining arguments | one `--name=value` per entry in the config's `custom` variables |
| Working directory | the plugin's own directory |
| Output | stdout **and** stderr are captured together and returned to the app |
| Result | shown as an on-screen toast |
| Failure | `Plugin error: <message>` followed by the captured output |

Path safety: the plugin name must be a single directory name (no `/`, `\` or
`..`), and `entry` is resolved *inside* the plugin directory — a manifest
cannot point at `/bin/sh`, another plugin or anything above its own folder. The
entry must be an existing regular file.

Two limitations worth knowing before you design a plugin:

- **The call blocks the UI.** The script runs on the frame that activated it;
  a slow plugin freezes the app until it exits. Keep work under a second, or
  print a result and let the next action poll.
- **Only `.sh` and `.py` entries execute.** Any other extension returns
  `Plugin "<name>" action "<action>" executed (entry=<path>)` without running
  anything, so a typo in `entry` looks like success rather than a failure.
  Note also that the stock TrimUI OS has **no Python** — on the handheld, `.py`
  plugins need a Python you install yourself, so prefer `sh`.

## Wiring a plugin into the UI

Plugins are reached through the normal config: a button whose trigger is
`plugin_action`, with `triggerTarget` naming the plugin directory and
`triggerValue` naming the action.

```json
{
  "type": "button",
  "text": "Say hello",
  "trigger": "plugin_action",
  "triggerTarget": "hello",
  "triggerValue": "greet"
}
```

The **Plugins** screen in the shipped config lists what was found and offers a
refresh button:

```json
[
  { "type": "textlog", "variable": "plugin_list", "x": 24, "y": 72, "width": "1232", "height": "539" },
  { "type": "button", "text": "Refresh", "trigger": "plugin_refresh", "x": 60, "y": 611, "width": "140", "height": "45" }
]
```

Entering a scene whose list variable is `plugin_list` (or pressing refresh)
rescans the directory and renders one line per plugin:

```text
- hello v1.0.0 (you) [enabled]
```

When nothing is found it shows
`No plugins found. Drop .py/.sh scripts into plugins/`.

---

## Worked example

`plugins/hello/plugin.json`:

```json
{
  "name": "hello",
  "version": "1.0.0",
  "description": "Greets the user, echoes the search query variable.",
  "author": "you",
  "entry": "hello.sh",
  "enabled": true
}
```

`plugins/hello/hello.sh`:

```sh
#!/bin/sh
# $1 is the action; the rest are --name=value pairs from the config.
action="$1"
shift

case "$action" in
  greet)
    echo "Hello, ${tspUsername:-friend}!"
    ;;
  env)
    # Every variable the app knows is available as an argument.
    echo "action=$action args=$*"
    ;;
  *)
    echo "Unknown action: $action"
    ;;
esac
```

The script does not need its executable bit for `sh entry.sh` to work, but
setting it (`chmod +x`) keeps the file usable from a terminal too.

Because the arguments are a `--name=value` list, a `sh` plugin can read one
directly:

```sh
for arg in "$@"; do
  case "$arg" in
    --search_query=*) query="${arg#*=}" ;;
  esac
done
```

## Safe mode

Plugins are the first thing safe mode disables. Turn it on with any of:

- `JukaHub --safe-mode`
- `JUKAHUB_SAFE_MODE=1`
- a `safe-mode.flag` file next to the application (easiest over USB or FTP)

While active, plugins are not loaded at all (the list renders empty), the
updater and tool installer do not start, and background network work is
skipped. Scenes, navigation, local media and settings keep working, and safe
mode never edits your config.

## Programmatic API

These functions exist in `player/plugins.go` and are what the UI path uses
internally. They are **not** exposed as triggers today — there is no on-device
install or uninstall screen yet:

| Function | Behaviour |
|----------|-----------|
| `LoadPlugins(config)` | rescan `plugins/`, honour `enabled`, skip everything in safe mode |
| `GetPlugins()` | the loaded plugins, keyed by directory name |
| `ExecutePluginAction(plugin, action, vars)` | run the entry point and return its output |
| `InstallPlugin(sourcePath, destName)` | copy one file into `plugins/<name>/`; refuses to overwrite |
| `UninstallPlugin(name)` | remove `plugins/<name>` |

If you call `InstallPlugin` / `UninstallPlugin` yourself, treat them as
destructive: they act on the real directories. Both validate the target name
(a plain file or directory name, nothing else).

### Not implemented yet

- `PluginContext` (config, toast, scene change and render callbacks) is
  declared in `player/plugins.go` but not passed to any plugin — a script
  cannot draw or change scenes; it returns text.
- The `config` block in `plugin.json` is parsed but not forwarded to scripts;
  use config variables (which *are* forwarded) instead.
- No timeout: a hung script hangs the frame that started it.

---

## Related

- [Configuration reference](CONFIGURATION.md) — the `plugin_action` / `plugin_refresh` triggers and variables
- [Packages](PACKAGES.md) — the declarative package format for shipping files, including tools a plugin may call
- [Security](../player/SECURITY.md) — how secrets are encrypted and kept out of configs
