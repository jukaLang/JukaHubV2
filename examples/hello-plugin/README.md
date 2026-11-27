# Hello plugin

The smallest complete JukaHub plugin. Copy this folder into `plugins/` (the
handheld's `Apps/JukaHub/plugins`, or the desktop player's working folder),
press Refresh on Settings → Plugins, and switch it on. Its **Say hello**
button then appears on the Plugins page and in the Start menu's Plugins group.

- `plugin.json` uses manifest `schemaVersion` 1 and asks for no permissions.
- `main.sh` receives the action id as `$1` and prints one line, which becomes
  the toast.

Start your own with `JukaHub --new-plugin my-plugin` instead of copying this
one. [docs/PLUGINS.md](../../docs/PLUGINS.md) documents the manifest, the
permission names and the action contract.