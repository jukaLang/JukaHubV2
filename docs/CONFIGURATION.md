# Configuration reference

JukaHub is driven by JSON: the web builder writes the layout, the player writes
your settings. Changing the file changes the app — no rebuild.

---

## Files

| File | Written by | Holds |
|------|-----------|-------|
| `jukaconfig.json` | web builder, hand edits | scenes, elements, variables — the interface |
| `jukaconfig.xml` | web builder (XML export) | the same, in XML |
| `jukauser.json` | the player | your mutable settings; survives updates |
| `jukaconfig.json.bak` | the player (`AtomicWrite`) | previous config, used if the main file is corrupt |

All four live next to the executable (`Apps/JukaHub/` on the handheld).

**Which file wins.** When both `jukaconfig.json` and `jukaconfig.xml` exist, the
one with the newer modification time is loaded; on a tie the JSON is used,
because that is the file the player itself saves. If the chosen file does not
parse, the other format is tried, and then the `.bak`. The loader never
silently discards a project: a broken XML export falls back to the JSON and
says so in the log.

`player/.gitignore` lists both config files: a config can hold API keys and a
Discord token. `jukaconfig.json` is committed anyway (it is the shipped UI), so
keep secrets out of it — `jukauser.json` is yours and is never committed.

**Schema version.** Every config carries `schemaVersion`; the current format is
**1**. A file without the field is treated as version 0 and migrated in memory.
A file written by a *newer* build is refused rather than downgraded (which
would otherwise overwrite it on the next save).

---

## Top level

| Key | Type | Meaning |
|-----|------|---------|
| `schemaVersion` | int | config format; see above |
| `AppName` | string | app name shown in the header/about |
| `Version` | string | design version, shown next to the brand |
| `Width`, `Height` | int | logical canvas (the shipped config uses 1280×720) |
| `Background` | string | background image path |
| `FontPath` | string | default font file |
| `title`, `author`, `description` | string | project metadata |
| `variables` | object | design tokens and settings, see below |
| `channel_profile` | object | `channel_id` and optional `token` for the JukaLand chat channel |
| `scenes` | array | the screens, see below |

## `variables`

Known keys are typed — they have their own JSON tags and are re-saved as
themselves. **Any other key is kept verbatim in `variables.custom`** and is
available to `$` substitution, which is how the builder stores per-project
settings such as `search_query` or `stream_url`.

| Key | Type | Meaning |
|-----|------|---------|
| `buttonColor`, `labelColor`, `inputColor` | `"#rrggbb"` or `{r,g,b}` | palette |
| `backgroundImage` | string | background image path |
| `fonts`, `fontSizes` | object | per-role font file and size (e.g. `small`, `medium`, `big`) |
| `fullscreen` | bool | start in fullscreen |
| `screenWidth`, `screenHeight` | int | window size on desktop. On supported TrimUI models the runtime uses the device's canvas: 1280×720 on the Smart Pro / Smart Pro S and a compact 960×720 canvas scaled to the 1024×768 Brick, Brick Pro and Hammer panels. Logical font sizes follow that canvas (0.75× on the compact one) so text keeps its proportion to the layout. |
| `tools_path` | string | where yt-dlp / ffmpeg live |
| `fileExplorerRoot` | string | starting directory of the file explorer |
| `weatherEnabled`, `weatherUnit` | bool, `""`/`"C"`/`"F"` | status-bar weather; empty picks C/F from the detected country |
| `tspUsername` | string | name used in the greeting |
| `playbackResolution` | string | maximum video height, e.g. `"720"` |
| `reducedMotion`, `lowPower` | bool | fewer animations / slower frame cadence |
| `gridColumns`, `searchWidth` | int | Home grid width, search box width |
| `dynamicBg` | bool | animated, weather-aware background instead of the ambient one |
| `screensaverTimeout` | int | seconds of idleness before the screensaver (or the [ambient screen](AMBIENT.md), which replaces it by default); 25 by default |
| `socialNotifs` | bool | periodic social notifications |
| `netPlayEnabled`, `videoPlayerEnabled`, `screensaverEnabled`, `launcherEnabled`, `externalAppEnabled`, `packageRepoEnabled`, `themeGalleryEnabled` | bool | feature toggles |
| `deviceModel`, `targetPlatform`, `defaultOrientation`, `accentColor` | string | device hints; unknown values mean "no preference", never an error |

Colors accept either form: `"buttonColor": "#1E293B"` or
`"buttonColor": {"r":30,"g":41,"b":59}`.

Reading a variable is case-insensitive, and the capitalized `Custom` block the
web builder emits (`variables.Custom.ButtonColor`) is flattened on load, so a
regenerated config resolves exactly like a hand-written one. Retired keys such
as `audioBackend` are dropped instead of being parked in `custom` and re-saved
forever.

The player writes your live settings (theme, volume, resolution, and anything
else a toggle changes) into `jukauser.json`, which is merged over the design
defaults at startup. Settings → **Smart Widgets** adds `batterySaver`,
`timeOfDayTheme` and `autoStart` there, for example.

`hapticsEnabled` (Settings → General → **Vibration**, on by default) turns the
short vibration feedback on or off. Detection checks the handheld's own motor
nodes first — `/sys/class/motor/voltage` plus `/sys/class/gpio/gpio227/value` on
the TG5040 family, `/sys/class/motor/level` plus gpio236 on the TG5050 — and
falls back to SDL controller rumble for a USB or Bluetooth pad. The row is
injected only when one of those answered, so hardware without a motor never
shows a switch that cannot do anything; the startup log says which path was
chosen (`[HAPTIC] vibration ready on …`). Because it is an ordinary toggle
variable, a hand-edited `"hapticsEnabled": false` in `jukauser.json` behaves
the same as the switch.

`autoStart` (Settings → Smart Widgets → **Start JukaHub at login**, off by
default) writes exactly one launcher and removes exactly that file again:

| Platform | Hook file |
|----------|-----------|
| TrimUI / CrossMix | `<SD>/System/starts/jukahub-autostart.sh` |
| Linux, macOS | `~/.config/autostart/jukahub.desktop` |
| Windows | `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\JukaHub.cmd` |

The handheld hook is a `#!/bin/sh` script that returns immediately and does its
work in a background subshell, so a slow or hung launch can never block the
firmware's boot. The subshell waits up to 30 s for the stock launcher to appear
(the hook may run before MainUI is on screen), then runs `launch.sh`, which
takes the display and hands it back on exit. Output goes to
`Apps/JukaHub/autostart.log`. Other files in `System/starts` are never touched,
and if the SD card cannot be located the toggle stays off and says why rather
than writing somewhere arbitrary.

Quick Settings (`Ctrl+Q`) writes these keys:

| Key | Meaning |
|-----|---------|
| `volume` (0–100) + `volumeSet` | mixer level; written only after you move the slider |
| `brightness` (0–100) + `brightnessSet` | backlight level, same rule |
| `screensaverTimeout` | idle seconds before the screensaver overlay, cycled by the panel and by Settings → General → Screensaver after |
| `lowPower`, `reducedMotion`, `fullscreen` | the remaining switches |
| `ledSync` | audio-reactive stick/ring LEDs |

The `*Set` flags separate "never touched" from a real 0, so a fresh install
never overrides the device's own mixer or backlight on first launch. A level is
only restored at startup when its flag is set.

Radio switches are **not** replayed at startup: airplane mode is applied when
you press it, not on every boot, and turning it off restores the Wi-Fi/Bluetooth
state that was active before it was enabled.

Each row picks its backend at runtime from a fixed list: `nmcli` then `rfkill`
for Wi-Fi, `bluetoothctl` then `rfkill` for Bluetooth, `rfkill` for airplane
mode, `pactl` then `amixer` for volume, and `/sys/class/backlight` for
brightness (only direct children of that directory are ever written, and only
the `brightness` file inside them). A row with no backend renders dimmed with
the reason, and pressing it says so instead of pretending to work.

Dock and ambient settings are plain custom keys, documented in
[Dock & ambient](AMBIENT.md): `ambientMode` (bool, default true) chooses
between the ambient idle screen and the starfield screensaver, `ambientWeather`
(default true) shows the dock forecast, and `ambientPhotos` points the slideshow
at a folder.

Time-lapse recording is two custom keys, documented in
[Time-lapse](TIMELAPSE.md): `timelapse` (bool, default true) films every game
launch, and `timelapse_interval` is the seconds between stills (default 20,
clamped to 3–300).

`ledSync` is opt-in and writes only inside `/sys/class/leds`: LEDs exposing
`multi_intensity` or `color`, plus brightness-only LEDs whose names look like
stick or ring lights. The colour comes from the RMS level and zero-crossing
frequency of the PCM the video and music path is already handing to SDL — no
microphone is captured, and the audio thread is never blocked by the lights.
Frames are quantised to 32 steps and unchanged frames are skipped, so the loop
runs at 25 fps without hammering sysfs. Stopping the sync, or quitting the app,
blanks the LEDs instead of leaving the last frame lit. With no addressable LED
the row is dimmed and the toggle refuses with the reason.

### Substitution

`$name` in any text, color or label is replaced with that variable's value:
`"text": "Hello $tspUsername"`. Lookup order is `custom` first, then the typed
fields, then fonts and font sizes. A name that does not resolve is left as the
literal `$name` so a typo is visible rather than silently empty.

## `scenes`

| Key | Meaning |
|-----|---------|
| `name` | scene identifier; `change_scene:<name>` targets it |
| `icon`, `description` | shown in menus and search |
| `background` | per-scene background color/path |
| `layout` | `"home"` switches to the tile home layout; empty is the standard panel layout |
| `elements` | the widgets on the screen |

## `elements`

| Field | Type | Notes |
|-------|------|-------|
| `type` | string | see the table below. An unknown type is logged and skipped |
| `text` | string | label/caption/button text |
| `x`, `y` | number or numeric string | accepts `30` and `"30"` (a `px` suffix is tolerated) |
| `width`, `height` | string or int | accepts `"280"` and `280` |
| `color`, `bgColor` | string | hex, or `$variable` |
| `font` | string | role name from `variables.fonts` / `fontSizes` |
| `opacity` | 0–1 | absent means opaque; an explicit `0` is invisible |
| `trigger` | string | what pressing the element does; see below |
| `triggerTarget` | string | target of the trigger (scene name, URL, video, plugin name) |
| `triggerValue` | string | second argument (action name, tab, category, default toggle state) |
| `sceneChange` | string | shorthand for `change_scene` with this target |
| `variable` | string | the config variable the element reads or writes |
| `listVariable` | string | variable a list element draws from |
| `columns`, `rows` | int | grid/list geometry |
| `placeholder` | string | input hint text |
| `style` | string | `"tile"` renders the OS-style app tile |
| `icon` | string | monogram/short glyph for tiles |
| `source`, `jsonPath` | string | data source for text/list elements |
| `autoRefresh` | bool | re-run the trigger on a timer where supported |
| `image`, `video` | string | media path/URL |
| `mediaVariable`, `videoVariable` | string | variable holding the media URL |
| `externalAppPath`, `externalAppReturn` | string | program to launch for `external_app` |
| `variableChange`, `variableChangeValue` | string | variable to set when the element is activated |
| `command` | string | command run by command-style elements |

### Element types

| Type | What it draws |
|------|---------------|
| `label` | text line |
| `button` | focusable button; runs its trigger |
| `toggle` | on/off switch bound to `variable` (default from `triggerValue`) |
| `input` | text field bound to `variable` |
| `searchresults` | YouTube result list |
| `dynamiclist` | list from `listVariable` (files, IPTV channels, apps, packages) |
| `textlog` | scrolling text from a variable (logs, plugin list, command output) |
| `textbrowser` | system/web/binary data browser |
| `piechart` | disk usage chart |
| `favorites` | saved favourites |
| `unitconverter` | unit conversion panel |
| `chat` | JukaLand chat |
| `shortslist` | YouTube Shorts grid |
| `canvas` | canvas sandbox |
| `packagelist` | Patch package list |
| `recent` | recently played |
| `image`, `video` | image and video elements |
| `themegallery` | theme preset picker |

### Triggers

| Trigger | Action |
|---------|--------|
| `change_scene` (or `change_scene:<Scene>`) | open another scene |
| `back`, `go_back` | same as B / the header back pill |
| `exit` | graceful shutdown (persists favourites first) |
| `set_variable` | write a value into a variable; `fullscreen`, `buttonColor`, `labelColor` and `weatherUnit` act immediately |
| `save_config` | save settings and confirm with a toast |
| `theme_preset` | apply the theme named in `triggerTarget` |
| `yt_search`, `youtube_search`, `youtube_smart`, `youtube_trending`, `youtube_play`, `play_video_from_var`, `play_focused`, `shorts_fetch`, `shorts_play`, `shorts_refresh` | YouTube search, trending, playback and Shorts |
| `shorts_open` | open the Shorts feed, pre-load it and start playing (the Media scene's **Shorts** button) |
| `custom_link`, `ip_stream`, `play_video_from_var` | play a typed/pasted URL, a stream URL, or a URL held in a variable |
| `music_player`, `podcast_load`, `iptv_load` | audio player, podcasts, Live TV playlists |
| `canvas_run`, `canvas_clear`, `canvas_open`, `jukaland_init` | canvas sandbox and JukaLand |
| `unit_convert`, `unit_swap`, `unit_category` | unit converter |
| `fav_tab`, `fav_play`, `fav_remove` | favourites tabs (`videos`/`recent`/`files`/`iptv`), play, remove |
| `fe_list`, `fe_up` | file explorer: list the current directory, go up one level |
| `app_refresh`, `app_open_folder` | rescan the Apps list, open the apps folder |
| `chat_send`, `chat_clear`, `groq_chat`, `discord_connect`, `attach_image`, `send_to_chat` | chat and assistant |
| `tick`, `add_ticker`, `clear_tickers` | stock/crypto tickers |
| `netspeed_run`, `benchmark_run`, `benchmark_random`, `hw_load`, `cron_load`, `cron_startup` | network test, benchmarks, hardware info, scheduled tasks |
| `terminal_run`, `emulator_run`, `external_app` | run a command, an emulator, or an external program |
| `log_export`, `log_clear` | export or clear the app log |
| `cache_clear` | clear the on-disk cache |
| `plugin_action`, `plugin_refresh` | run a plugin action, rescan the plugins directory — see [Plugin API](PLUGINS.md) |
| `packages_refresh` | refresh the Patch package index |
| `textbrowser_refresh` | reload the textbrowser from its source |
| `textbrowser_system`, `_zeroconf`, `_json`, `_processes`, `_network`, `_temps`, `_services`, `_files`, `_copy`, `_clear` | textbrowser data sources and clipboard |
| `tools_open`, `processes_open`, `wifi_open`, `ip_open`, `ip_add`, `ip_fetch` | the built-in tool panels (including the [Wi-Fi picker](WIFI.md)) |
| `companion_open` | the phone companion QR card (toggles) |
| `ftp_toggle`, `ftp_start`, `ftp_stop`, `ssh_toggle`, `ssh_start`, `ssh_stop` | FTP and SSH/SFTP servers |
| anything starting with `http://` or `https://` | opens the URL in the system browser |

An unrecognised trigger does nothing (and is not logged as an error), so a
config from a newer builder degrades to an inert button instead of a crash.

---

## Power tools data

**Tools → Power tools** keeps its own small files next to `jukauser.json`.
They are plain JSON/text so they can be backed up or edited over FTP/SFTP.

| File / setting | Meaning |
|----------------|---------|
| `power-scripts.json` | Saved snippets, an array of `{"name": "…", "command": "…"}`. Built-ins are compiled in and cannot be deleted |
| `power-bench.json` | Benchmark entries (`device`, `kind`, `write`, `read`, `at`, `version`), newest 50 kept, sorted by the slower of read/write |
| `power-config-history/` | The 20 newest `jukauser.json` snapshots. A save only creates one when the content changed |
| `variables.Custom.powerSyncSource` | rsync source folder; defaults to the JukaHub folder when unset |
| `variables.Custom.powerSyncTarget` | rsync target: a folder or `user@host:/path` |
| `variables.Custom.powerBenchURL` | Optional HTTP(S) URL returning a JSON array of benchmark entries for the online board; `JUKAHUB_BENCH_URL` wins when both are set |

Snippets run through `sh -c` (or `cmd /c` on Windows) with a 30-second
timeout and the app's permissions. rsync is invoked directly with `-az
--itemize-changes`, never through a shell, and never with `--delete`.

## Privacy and diagnostics

Anonymous diagnostics are controlled by `variables.Custom.telemetryOptIn` in
`jukauser.json` and are **off** unless explicitly enabled in **Tools → Community
& support**. When enabled, the payload contains only:

| Field | Example |
|-------|---------|
| `kind` | `device` on launch, `crash` on a panic |
| `version`, `release` | `v0.4.0`, `JukaHub-20261017…` |
| `device`, `platform` | `TrimUI Smart Pro`, `Trimui` |
| `os`, `arch` | `linux`, `arm64` |
| `crash` | `panic` (crash events only) |

No install ID, account, config value, filename, URL or usage history is sent.
A collector must be configured by the build (`-X main.telemetryEndpoint=…`) or
at runtime with `JUKAHUB_TELEMETRY_URL`; when neither is set the switch stores
the preference but sends nothing.

**Report a bug** (same panel) builds a GitHub issue from `DiagnosticsText`: app
and release version, build date, device, OS/architecture, config scene/element
counts, safe-mode state and backup count. It never includes config values,
tokens, usernames or private paths.

---

## Related

- [Plugin API](PLUGINS.md) — plugin manifest, entry points and execution contract
- [Controls](CONTROLS.md) — the buttons that drive these elements
- [Building](BUILDING.md) — how the shipped `jukaconfig.json` is produced
