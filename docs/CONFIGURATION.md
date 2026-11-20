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
| `variables.Custom.voiceServer` | string | Mumble server (`host:port`) for Chat's Voice tab and private Voice Channels; unset means the free public default `mumble.gamemania.org:42069`. Set from Chat → Voice → Voice server. `JUKAHUB_VOICE_SERVER` overrides it. See [Voice](VOICE_CHANNEL.md). |
| `variables.Custom.voiceRoom` | string | Channel path of the voice room on that server, last part being the room; unset means `Private User-Channels/JukaHub`. Private Voice Channels open next to it. |
| `variables.Custom.voiceRelayURL` | string | Optional self-hosted relay for private Voice Channels (the voice room always uses the voice server), a secure WebSocket base URL such as `wss://voice.example.com/voice`; set from Chat → Voice → Own voice relay, empty clears it. `JUKAHUB_VOICE_RELAY` overrides it. See [Voice](VOICE_CHANNEL.md). |
| `scenes` | array | the screens, see below |

## `variables`

Known keys are typed — they have their own JSON tags and are re-saved as
themselves. **Any other key is kept verbatim in `variables.custom`** and is
available to `$` substitution, which is how the builder stores per-project
settings such as `search_query` or `stream_url`.

| Key | Type | Meaning |
|-----|------|---------|
| `buttonColor`, `labelColor`, `inputColor` | `"#rrggbb"` or `{r,g,b}` | palette. `buttonColor` is a light tint under each scene button's glass; focus rings, pills and highlights use the active theme's accent, not this colour |
| `backgroundImage` | string | your own wallpaper, drawn on every screen (Home included) under the backdrop's dim and colour blooms so glass and text stay readable. Empty or the stock `background.jpg` means Home's procedural backdrop; the stock image is not loaded. A picked image is scaled once to the canvas when it loads. A `.gif` plays: it is decoded frame by frame off the render thread, cropped to the screen's shape and kept within a memory budget (40 MB of frames on the handheld), and pixel art stays sharp at a whole-number scale (see `wallpaperAnimate`) |
| `wallpaper` | string | the picture picked in **Settings → Theme & look → Wallpaper** (or Start → Device → Wallpapers), kept in `jukauser.json` and put back at start when the design config names the stock `background.jpg`; `none` means no wallpaper. Whichever came last, the pick or an experience pack's wallpaper, wins; a wallpaper named in the design config itself still beats both |
| `wallpaperAnimate` | bool | Settings → Theme & look → **Animate wallpaper** (on by default): a GIF wallpaper plays; off holds its first frame. Battery Saver holds it too. Plain Reduce Motion does not, because the handheld ships with it on |
| `themeAutoUpdate` | bool | Settings → Theme & look → **Update themes automatically** (on by default): a theme installed from the gallery (Community themes) gets its author's new version when the gallery index is read — on opening the panel, and once a day in the background while online. A theme on screen repaints itself. A theme file you edited on the device is never replaced automatically: the panel tags it **Edited · A update**, and A asks once more, keeping your file as `themes/<id>.edited.json.bak`. Off: new versions are only tagged **A update** in the panel |
| `fonts`, `fontSizes` | object | per-role font file and size (e.g. `small`, `medium`, `big`) |
| `fullscreen` | bool | start in fullscreen |
| `screenWidth`, `screenHeight` | int | window size on desktop. On supported TrimUI models the runtime uses the device's canvas: 1280×720 on the Smart Pro / Smart Pro S and a compact 960×720 canvas scaled to the 1024×768 Brick, Brick Pro and Hammer panels. Logical font sizes follow that canvas (0.75× on the compact one) so text keeps its proportion to the layout. |
| `tools_path` | string | where yt-dlp / ffmpeg live |
| `fileExplorerRoot` | string | starting directory of the file explorer |
| `weatherEnabled`, `weatherUnit` | bool, `""`/`"C"`/`"F"` | status-bar weather; empty picks C/F from the detected country. In Settings → General, press Weather Unit to cycle Auto, Celsius and Fahrenheit. |
| `tspUsername` | string | name used in the greeting |
| `playbackResolution` | string | maximum video height, e.g. `"720"` |
| `subtitleLanguage` | string | Subtitle language code for online video (Settings → General), e.g. `"en"`, `"es"`, or `"pt-BR"`; blank defaults to English. CC fetches a written track in that language, another region of it, the auto-generated track or a YouTube translation — from YouTube's caption list first, yt-dlp second — and caches it under the data folder's `subtitles/`; downloaded videos keep theirs beside the file as `<name>.<lang>.srt`. The code must start with a letter (`all` is refused). |
| `uiLanguage` | string | Interface language (Settings → General → Language): `en` (default), `es`, `pt-BR`, `fr`, `de`, `it`, `ru`; unknown values mean English. Web pages, PDFs, chat, notes, subtitles, typed text, file names and game titles are never translated. |
| `reducedMotion`, `lowPower` | bool | fewer animations / slower frame cadence |
| `gridColumns`, `searchWidth` | int | Home grid width, search box width |
| `dynamicBg` | bool | animated, weather-aware background instead of the ambient one |
| `liquidBg` | `"off"`/`"slight"`/`"medium"`/`"lively"` | Settings → Theme & look → **Liquid background**, or **R2** on any screen whose footer gives R2 no other job (the footer then shows **R2 Water**; **W** on Home with a keyboard), which steps Off → Slight → Medium → Lively (default `off` when unset; the old `gentle` reads as `medium`). Once on, the water is behind every screen: the backdrop under the glass becomes a shallow pool that the wallpaper or Home's colours are bent through. Moving the focus drops a ripple under the new tile (a step where no focus ring moves still ripples near the highlight), choosing something splashes, going back swells the water up, the Smart Pro's sticks stir it (the pointer on a Brick in mouse mode, the mouse on a PC), and occasional rain keeps it moving until two minutes after you last touched anything. An unrecognised value reads as off. Reduce Motion halves the rain and softens the ripples. The water is drawn between its steps, its light and shadow over the whole backdrop, and while it moves it asks for the full frame rate; Low Power uses a coarser grid, and the handheld steps down to a coarser one still by itself if the water costs more than 5 ms a frame |
| `liquidBgResume` | `"slight"`/`"medium"`/`"lively"` | Last active liquid level, saved when **Liquid background** is switched off. Start → Device → **Liquid background** restores it when switched back on; if absent, the switch uses `medium` |
| `liquidTilt` | bool | **Tilt to slosh** (default on): with the liquid background on, tilting a device that has an accelerometer (Linux IIO under `/sys/bus/iio`, or an SDL sensor on a Windows tablet) sloshes the water. The row appears only where a sensor answered; none is known on the Smart Pro or the Brick |
| `screensaverTimeout` | int | seconds of idleness before the screensaver (or the [ambient screen](AMBIENT.md), which replaces it by default); 25 by default |
| `autoSleep` | minutes as text, `"0"`/`"3"`/`"5"`/`"10"`/`"15"`/`"30"` | Settings → Power → **Auto sleep** (handheld only; default `"0"`, Off): suspend the handheld after that many minutes without input — never while a game owns the screen, while charging, while music or a video plays (or is paused), while a download, install, extraction or Screen Share viewer is running, while a panel is open, or while any alarm is enabled (a sleeping device cannot ring). The wake counts as input. |
| `travelLock` | bool | Settings → Power & battery → **Travel lock** (TrimUI Smart Pro only; default off): the FN switch locks the power button. Turning it on stores the switch's current position as unlocked (`travelLockAt`, bookkeeping: the reading `"0"`/`"1"` that means locked). Locked, a press does not wake the device (each wake but an alarm's goes back to sleep) and a power-on is turned straight back off by the boot hook `<SD>/System/starts/00-jukahub-travel-lock.sh`, which the setting writes and removes (it logs to `travel-lock.log` in the JukaHub folder). Slide the switch back, or delete the hook, to undo. |
| `sleepPowerOff` | text: whole hours (`"1"`…`"48"`) or minutes with an `m` (`"15m"`, `"30m"`); `"0"` is Off | Settings → Power & battery → **Power off after sleeping**, and Quick Settings → **Auto power off** (handheld only; stops Off, 15 min, 30 min, 1, 2, 4, 6, 8, 12, 24 and 48 h; missing = `"6"`, or `"1"` on a kernel that only light-sleeps; a hand-edited delay over a week is held at a week): after that long asleep the handheld wakes itself on the RTC alarm with the screen off, saves the RetroArch game the sleep was holding (save state + quit through RetroArch's network commands; only RetroArch's exit counts as proof) and powers off. On a charger, or with a game it cannot save that way (a standalone emulator, a port, network commands off, the in-game menu open), it goes back to sleep instead. Time asleep is the longer of BOOTTIME's and the RTC's count (a kernel that leaves the suspend out of BOOTTIME no longer hides the sleep). A NextUI pak game sleeping in minarch gets the same deadline: when minarch saves the game for its sleep (NextUI's `auto_resume.txt` naming the running game, written with its slot-9 state) the RTC alarm is set for the delay, and a wake on that alarm (no power press, no JukaHub alarm due) powers the device off, on a charger too; the marker the wake removed is put back so the next power-on resumes the game. A core without save states, or a marker JukaHub did not see before the suspend, gets no deadline. In Quick Settings, A turns it Off or back on at the last delay (kept in the bookkeeping key `sleepPowerOffLast`) and Left/Right step it. Needs an RTC wake alarm; the row says when there is none |
| `resumeAtPowerOn` | bool | Settings → Power & battery → **Resume game at power on** (default on): the game that power off saved starts again at the next power-on, boot animation skipped, back at its state through RetroArch's auto-load (needs `savestateResume`). Hold **B** at power-on to skip. The one-shot marker `data/resume-at-power-on.json` is deleted as soon as it is read |
| `saveFlushSeconds` | number | Seconds between RetroArch's writes of a changed in-game (battery) save during games started from JukaHub (default `10`, at most `3600`; `0` leaves RetroArch's own setting alone). Sets `autosave_interval` for that launch only, and only when your own `retroarch.cfg` value is missing or 0; your value comes back when the game ends. RetroArch writes only when the save changed. See [Pick up and play](PICK_UP_AND_PLAY.md#your-progress-is-safe) |
| `powerKeyMusic` | `"screen-off"`/`"sleep"` | Settings → Power & battery → **Power button while music plays** (handheld only; default `"screen-off"`): with music playing and no game, the power button only turns the screen off and the music keeps playing ([MUSIC_PLAYER.md](MUSIC_PLAYER.md#screen-off)); `"sleep"` sleeps and pauses it |
| `clock12`, `dateFormat` | bool, `""`/`"dmy"`/`"mdy"`/`"ymd"`/`"dots"` | Settings → Date & time: the 12-hour clock, and the order dates are written in (`9 Nov 2026`, `Nov 9, 2026`, `2026-11-09`, `09.11.2026`); empty follows the country your connection is in |
| `netTimeSync` | bool | Settings → Date & time → **Set time automatically** (on by default; shown only where JukaHub may set the clock — the handheld, running as root): SNTP when Wi-Fi connects and hourly (after a failure: again in 1, 2, 5, 10, 30 minutes until one works), the HTTPS date as a fallback; a correction of more than 10 minutes is made only when the HTTPS date confirms it. On Windows, macOS and Linux PCs JukaHub never asks a time server by itself; there the row shows the system's own switch (read from the Windows Time service on Windows) and opens the system's date & time page. **Set date & time** sets the clock by hand on the handheld (and turns this switch off, so the next sync does not undo it); elsewhere it opens the system's page. The time zone chosen on the device is not a config key: it lives in `jukatimezone` next to the binary, which `launch.sh` exports as `TZ` (programs JukaHub starts get a newly chosen zone after JukaHub restarts) |
| `liveBuilder`, `liveBuilderLAN` | bool | Settings → Network → **Live Builder**: whether the web builder's receiver starts with JukaHub (default off on the handheld, on on a PC; either way it switches itself off after 30 minutes without use), and whether a PC listens on the network instead of that computer only. `JUKAHUB_LIVE_BUILDER` and `JUKAHUB_LIVE_LAN` win over both, and the older `liveBuilderDisabled: true` still keeps it off; see [SECURITY.md](../SECURITY.md#live-builder-the-web-builders-receiver) |
| `usbFileTransfer` | bool | Settings → Network → **USB file transfer** (default on, handheld only): a USB cable to a computer opens the SD card there over MTP; off, the cable only charges. See [USB_FILE_TRANSFER.md](USB_FILE_TRANSFER.md) |
| `usbBrowserLink` | bool | Settings → Network → **Browser over USB** (default on, handheld only, shown while USB file transfer is on): the cable also carries a small network, so the computer's browser opens the Phone Companion page at `http://10.73.73.1`; needs RNDIS, ECM or NCM in the firmware's kernel. Switching it re-connects the port. See [USB_FILE_TRANSFER.md](USB_FILE_TRANSFER.md#open-jukahub-in-your-browser-over-the-cable) |
| `vpnEnabled`, `vpnRoute` | bool, `""`/`"config"` | Settings → Network → **VPN** (default off): send the device's traffic through the WireGuard configuration in `data/vpn.json` (imported in Settings, Files or the phone companion; never stored here), and **Send through the VPN**: everything (`""`) or only the configuration's `AllowedIPs` (`"config"`). On without a configuration does nothing; see [VPN.md](VPN.md) |
| `dnsMode`, `dnsServers` | `""`/`"cloudflare"`/`"google"`/`"quad9"`/`"adguard"`/`"family"`/`"opendns"`/`"custom"`, text | Settings → Network → **DNS** (default `""`, Automatic: the network's own servers): the servers that turn names into addresses — a public service, or `"custom"` with up to three IPv4/IPv6 addresses in `dnsServers` separated by spaces or commas (**Custom DNS servers**). On the handheld (as root) for the whole system through `resolv.conf`, kept after every DHCP lease and put back on Automatic, in safe mode and at quit; on Windows for JukaHub's own lookups. A VPN configuration with `DNS =` wins while it runs. Protected from pushed designs; see [DNS.md](DNS.md) |
| `libraryFingerprints` | bool | Settings → Games & emulators → **Fingerprint games in the background** (default on): the CRC32 of games up to 64 MB, read one at a time at the lowest priority while nothing plays and the battery is above 30% (or charging), kept in the library index `data/library-index.db` (**Rebuild library index** starts it over); game details and cover matching use it. Off: a game's CRC32 is read only when its page asks. See [GAME_LIBRARY.md](GAME_LIBRARY.md) |
| `protectStandaloneEmus` | bool | Settings → Power & battery → **Protect standalone emulators** (default on): at 3% battery, at Power off and at Power off after sleeping, a standalone emulator (PPSSPP, DraStic, Flycast, Mednafen, DuckStation, Mupen64Plus, ports, Wine games) is asked to quit cleanly, saving a state on the way out where its own option is on, before the card is synced; at 10% one that cannot save by itself is named. Last record: `data/sessionsafe-last.json`. See [SESSION_SAFETY.md](SESSION_SAFETY.md) |
| `narration`, `narrationRate`, `narrationHints`, `narrationToasts`, `narrationVoice` | bool, `"80"`…`"250"`, bool, bool, `"default"`/`"female"`/`"male"` | Settings → Accessibility → **Narration** (default off; hold L1 + R1 for 3 s or press Ctrl+Alt+Z to switch it from anywhere): speaks the focused control's name, value and what A does, with the offline voice (Windows' own voice on a PC); the speaking rate in percent, whether hints and pop-up messages are read, and the voice. See [ACCESSIBILITY.md](ACCESSIBILITY.md) |
| `coreUpdateMode` | `"off"`/`"notify"`/`"install"` | Settings → Games & emulators → **Emulator updates** (on the handheld the libretro cores, JukaHub's PPSSPP, hakuX, PortMaster's files and the downloaded runtimes; on a PC the emulators JukaHub installed and its RetroArch's cores; default `install`: once a day, while charging or idle — on a PC once JukaHub sits idle, not on a laptop's battery — and never during a game or a video, builds that differ are installed after a check that they can load here, and the replaced one is kept for a one-press roll back; each updater's last check is kept in `emu-autoupdate.json` beside the patch state; on a Smart Pro whose card never had an Xbox launcher, `install` also installs hakuX once, and `data/hakux/auto-install.json` then records that the card had one, so a deleted hakuX is not put back). Written once at start-up with the legacy `emuAutoUpdate` bool beside it (`true` only for `install`), so an older build never installs where this one would not; a stored `emuAutoUpdate` without a mode migrates (explicit off → Off, explicit on → Install automatically). A pick in Settings, on the Core builds page or in an imported settings file also writes `coreUpdateModeChosen: true`; a `"notify"` without it (what earlier builds stored by themselves) becomes `install` once, with a toast |
| `retroArchGlibc` | `"auto"`/`"always"`/`"off"` | Settings → Games & emulators → **RetroArch's C library** (64-bit TrimUI handhelds): which glibc RetroArch starts on when JukaHub launches a game — `auto` (default, **Automatic**: only a game whose core cannot load on the firmware's glibc 2.33 and can on JukaHub's private glibc 2.39, plus RetroArch's own menu once that runtime is installed; so the core updater installs the newest buildbot nightlies), `always` (**JukaHub's glibc 2.39**: every RetroArch launch), `off` (**Firmware's only**: never; builds that need a newer glibc are refused and put back). The runtime is the one in [`PRIVATE_GLIBC.md`](PRIVATE_GLIBC.md), downloaded the first time a core needs it; RetroArch runs on it inside the game's own mount namespace, the card's RetroArch is not changed, and a RetroArch that fails the start-up probe stays on the firmware's (`data/private-glibc/ra/probe.json`). Games started from the firmware's own menu still use the firmware's glibc |
| `emuPickAsk` | `"first"`/`"always"`/`"never"` | Settings → Games & emulators → **Ask which emulator**: how often a game whose console has more than one emulator or core (its own launchers, the other scripts its `Emus` folder lists, installed RetroArch cores that play it) asks **Which emulator?** — `first` (default: once per game; A plays and remembers it for that game, Y plays just once), `always` (every launch; Y always uses the one picked), `never` (the one used last, else the recommended one). A game's or console's pin always wins. Settings → Games & emulators → **Get more cores** sits beside it (handheld; which consoles were done is kept in `emu-morecores.json` beside the patch state, not in the config) |
| `google_client_id`, `google_client_secret` | string, string (`ENC:`…) | Your own Google OAuth client ("TVs and Limited Input devices") for Drive save sync, set from the phone (Tools → Save sync → **Set up Google sign-in**); used when `JUKAHUB_GOOGLE_CLIENT_ID` is not in the environment, before the client stamped into the build. The secret is encrypted with the device's key; neither is exported with settings. WebDAV keeps its sign-in in `data/webdav.json`, not the config. See [Cloud save sync](CLOUD_SYNC.md) |
| `artAutoFetch` | bool | Settings → Games & emulators → **Find box art automatically** (default on): background box-art passes from the libretro thumbnail set — about 100 s after start on a card that never had one (on a handheld where JukaHub keeps the time, once network time has set the clock or after half an hour), at most daily, and early when the ROM folders gain games; online only, never during a game, on battery only at 20 % or more and after 90 s without input. Only game titles are sent (GET requests to thumbnails.libretro.com). Off leaves the library's Fill gaps (**L1** / **F**). The pass stamp is `data/game-art/.auto-art.json` ([Games Library → Found automatically](GAME_LIBRARY.md#found-automatically)) |
| `gameplayCovers` | bool | Settings → Games & emulators → **Covers from gameplay** (default on): a library game with no cover at all gets a picture of itself read from the screen two minutes into play (handheld framebuffer; on Windows the game's window while it is in front), black bars trimmed, saved as `data/game-art/.gameplay/<System>/<rom stem>.png`. Black, white or flat frames, those within 30 s of Menu or while a library RetroArch reports PAUSED, and (before minute 4) still or mostly dark ones are skipped and retried every 20 s of play up to 8 minutes. Any real cover found later replaces it; the box-art fetch keeps looking. Off stops new pictures; taken ones stay. Nothing leaves the device. |
| `remotePlayAutoDownload` | bool | Settings → Games & emulators → **Download Remote Play automatically** (default on; shown only where Remote Play runs): while `bin/chiaki-stream` or `bin/chiaki-regist` is missing, JukaHub downloads them in the background about a minute after it starts — online, on screen (not behind a game or the idle screen) and with a clock past the build's date — and tries again after 2 to 60 minutes when the network, GitHub or the clock failed it. Off, they download when PS Remote Play opens. See [REMOTE_PLAY.md](REMOTE_PLAY.md#installing-the-helpers). |
| `ssh_readonly`, `ssh_full_root` | bool | the SFTP server refuses every write; or it shares the whole Linux file system instead of the SD card (like `JUKAHUB_SSH_FULL_ROOT=1`, while the **Share whole system…** switch on its panel lasts one run). A design pushed through the Live Builder that sets either waits for approval on the device |
| `castQuality` | `"smooth"`/`"balanced"`/`"sharp"` | Screen Share's picture: Smooth (640 px, 20 fps; the handheld's default), Balanced (960 px, 15 fps; a PC's default) or Sharp (full size, 10 fps). The handheld captures its menus at most about 12 times a second (fewer when the GPU readback is slow) and re-sends a screen that is not changing once a second |
| `castGames` | bool | Screen Share's **Games too** (handheld only, on by default): while a game owns the screen the picture comes from `/dev/fb0`, through the bundled ffmpeg when it is installed, else JukaHub's own reader at up to 8 fps and 640 px. Off shows viewers a pause card while you play |
| `castRotate` | `0`/`90`/`180`/`270` | turns a shared game's picture clockwise; unset turns a portrait framebuffer under a landscape canvas a quarter anticlockwise (the guess for a panel mounted sideways) |
| `castToken` | string | Screen Share's view-only link token (16 base32 characters), kept so a bookmarked link — a TV's, say — survives restarts; **Y** on the panel replaces it. A secret: exported configs redact it |
| `spatialMode` | `"off"`/`"auto"`/`"headphones"`/`"speakers"` | Settings → Sound & haptics → **Spatial audio**, also Tools → Audio (default `off`): virtual surround for everything JukaHub plays — videos, music, podcasts, audiobooks, YouTube, background music — and, with `spatialGames`, games and apps. `headphones` places four virtual loudspeakers and a small virtual room around you, `speakers` widens the stereo of a close pair of speakers, and `auto` picks between them by the output in use. Music's Bit-perfect bypasses it ([SPATIAL_AUDIO.md](SPATIAL_AUDIO.md)) |
| `spatialStrength` | 0–100 | Tools → Audio → **Spatial strength** (default 75; the row steps 25/50/75/100): how much of the effect you get — the far-ear crossfeed, the rear loudspeakers and the room in Headphones mode, the extra width in Speakers mode |
| `spatialRoom` | `"small"`/`"medium"`/`"large"` | Tools → Audio → **Virtual room** (default `medium`): the room the Headphones mode plays in, from close and dry to a hall |
| `spatialGames` | bool | Tools → Audio → **Spatial audio in games & apps** (default on; matters only while `spatialMode` is not `off`): games and apps started from JukaHub play through the same effect — on the handheld every program, through ALSA's LADSPA support once a check has proven it, else (and on Windows) RetroArch games as a DSP filter RetroArch loads for that launch only; the row says when the plugin or a route is missing ([SPATIAL_AUDIO.md](SPATIAL_AUDIO.md)) |
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

`timeOfDayTheme` (Settings → Theme & look → **Light & dark by time of
day**) switches between two colour themes: `themeDay` (default `light`) from
`themeDayStart` and `themeNight` (default `hub`, JukaHub's own glass) from
`themeNightStart`, both `"HH:MM"` (07:00 and 19:00 unless set; a window that
wraps midnight works too). The theme changes at those two times only, so a
theme picked by hand stands until the next switch — and becomes the theme of
that part of the day. High contrast, the colour-blind palettes, a total
conversion and the battery saver's OLED all pause it while they are on.

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

`mouseMode` (Settings → Controls → **Mouse mode**, on by default) lets the held
**Select + L1** chord, and **F6** on a keyboard, turn mouse mode on and off: a
pointer that stays on (left alone for 10 seconds it hides where it is until
the next move or scroll; the first **A** then only shows it) and that the
D-pad and both sticks move
([Controls](CONTROLS.md#mouse-mode-hold-select--l1)). Off, the chord does
nothing and L1 is always the shoulder.

`mouseInGames` (Settings → Controls → **Mouse in games and apps**, off by
default; the row is shown on TrimUI handhelds only) carries the chord into
games, emulators and ports: the pad becomes a uinput mouse ("JukaHub Mouse")
for the running program until the chord again or Menu. Only programs that
draw their own pointer show one.

`voiceMenuButton` (Settings → Controls → **Menu button: voice**, on by default;
the row is shown on handhelds only) makes the TrimUI's Menu button open the
voice orb, and hold-to-talk with it when `menuHoldScreenshot` is off
([Voice](VOICE.md#the-voice-orb)). Off, Menu goes back like B, as it did
before. On a keyboard F4 opens the orb either way.

`menuHoldScreenshot` (Settings → Controls → **Hold Menu: screenshot**, on by
default; handhelds only): holding Menu for 0.7 s and letting go saves the
screen as a PNG, in JukaHub's menus and in a game it launched. A tap still
opens the voice orb (or goes back, where Menu means Back). Shots go to the
`screenshots` folder inside JukaHub's own folder — `Apps/JukaHub/screenshots` on
a TrimUI, beside `JukaHub.exe` on a PC (`screenshot_<time>.png` for JukaHub,
`game_<time>.png` for a game). When that folder is read-only they go to
`/mnt/SDCARD/Screenshots` on a TrimUI and `Pictures\JukaHub` on a PC;
`JUKAHUB_SCREENSHOT_DIR` overrides all of them. The preview card
shows the saved file's path. In a game the picture is the one on screen when
Menu went down; a button, trigger or volume key pressed with Menu cancels it.

`selectR1Screenshot` (Settings → Controls → **Select + R1: screenshot**, on by
default; the row is shown on TrimUI handhelds only): pressing Select + R1
together in a game JukaHub launched saves the screen at once, no hold, as a
`game_<time>.png` in the same folder; each press is one picture, at most one a
second ([Controls](CONTROLS.md#screenshot-in-a-game-select--r1)). The game
sees the press too. In JukaHub's own menus the pair stays the control-profile
switch.

`openBooksWith` (Settings → Media & playback → **Open books with**, shown when
KOReader is installed in the card's Apps folder): `"jukahub"` (the default)
opens PDF and EPUB in JukaHub's reader; `"koreader"` sends them to KOReader
instead (Files, chat, game manuals). Open with… → Reader and Y on a manual
still use JukaHub's reader, and it is the fallback when KOReader does not
start. Formats JukaHub cannot show (comics, DjVu, FB2, MOBI/AZW, XPS, CHM,
Word) open in KOReader from Files whenever it is installed. JukaHub hands the
book to KOReader's launch script when the script passes its arguments on
(`"$1"`/`"$@"`); otherwise it runs `luajit reader.lua <book>` from KOReader's
folder.

`ctlModeScreen` (Controller Mode → **Screen while playing**): `"off"` (the
default) turns the panel off 15 seconds after a link starts forwarding (tap
Menu to light it), `"on"` keeps the live controller visible.
`ctlModeUSBShape` (Controller Mode → USB cable → **Shape**): `"xbox"` (the
default, an Xbox 360 controller) or `"hid"` (a generic gamepad). Layouts and
paired PCs live in `controller-mode/` in the data folder, not in this file
([Controller Mode](CONTROLLER_MODE.md)).

`jukaVoice` (Settings → Voice & AI → **Juka's voice**): `"voice"` (the
default) speaks the voice assistant's answers — the orb, the Voice command
page, the game assistant — `"all"` Chat's Juka AI replies as well, and `"off"`
none; `false` reads as off. `jukaVoiceName` (**Voice**): `"hannah"` (the
default), `"autumn"`, `"diana"`, `"austin"`, `"daniel"` or `"troy"` — Groq's
Orpheus voices — or `"device"` for the device's own voice where it has one
(Windows' speech on a PC; espeak-ng, espeak, flite, pico2wave or piper on
Linux, including the optional Piper voice downloaded from Voice & AI on a
TrimUI); anything else is the default. Groq's voice uses the same Groq key as
Chat ([Voice](VOICE.md#jukas-voice)).

`voiceShortcuts` (Settings → Voice & AI → **Voice shortcuts**; none by
default) is the list of words taught to the voice assistant, at most 50. Each
entry has `phrase` (what you say, up to 40 letters) and `kind` — `screen`,
`setting`, `app`, `game`, `file`, `media`, `command` or `web` — with, by kind,
`title` and `where` (the row's title and subtitle), `path` and `system` (games
and files), `command` (a voice command line) or `url` (http or https). An
entry that cannot be used is skipped, and a second entry with the same phrase
is dropped ([Voice](VOICE.md#your-own-words)).

`gameMenu` (Settings → Controls → **In-game menu**, on by default; TrimUI
handhelds) makes holding **Select + L2** in a game pause it under a menu:
Resume game, Ask Juka, Quit to JukaHub
([Controls](CONTROLS.md#in-game-menu-hold-select--l2)).

`gameSwitcher` (Settings → Games & emulators → **Game switcher**, on by default)
is the Onion / NextUI-style strip of recent games with save-state pictures: X on
Home (Standard and Hero layouts), Start → Games, search, and the in-game menu's
**Game switcher** row (quits the game, then opens the strip). Off hides the
rows and gives X on Home back.

Settings → **Emulation** writes these custom keys; each value goes into
`retroarch.cfg` (or the core-options file) for one library launch and is undone
when RetroArch exits, like `videoFilter`:

| Key | Default | Meaning |
|---|---|---|
| `raTuningAudio` | `auto` | resampling: `auto` (per console), `best` (sinc, high), `balanced` (sinc, lower), `fast` (cc), `own` |
| `raTuningLatency` | `auto` | `auto`, `standard` (RetroArch's own), `low` (2 swap images, automatic frame delay, audio ≤ 64 ms), `lowest` (audio ≤ 48 ms, plus 1 frame of run-ahead on light consoles) |
| `raTuningOverlay` | `own` | `own`, `none`, or the path of an overlay `.cfg` |
| `raTuningSmooth` | `true` | v-sync, audio sync and rate control on, VRR runloop off |
| `raTuningRumble` | `own` | `own` or 0–100 (`input_rumble_gain`) |
| `raTuningRotate` | `upright` | `upright`, `tate` (FBNeo / MAME 2003-Plus vertical mode), `own` |
| `raTuningPAL` | `false` | the PAL region core option where the core has one |
| `raTuningCrop` | `own` | Crop overscan: `on` (`video_crop_overscan = true` for the launch), `off` (`false`: the whole picture), `own` (RetroArch's own value, which crops by default) |
| `videoFilter` | `auto` | the look (also Games & emulators → Video filter): `auto` (by console), `unmanaged` (RetroArch's own), `sharp`, `smooth`, `pixel`, `scanlines`, `lcd`, `crt`, `pt` ([Looks](LOOKS.md)) |
| `raTuning.<console>.audio` / `.latency` / `.overlay` / `.look` / `.crop` | "" | one console's own choice ("Adjust for"); "" follows the all-consoles value; `.crop` is `on` or `off` |

Cheats switched on in Game options → **Cheats** set `apply_cheats_after_load`,
`apply_cheats_after_toggle` and (only when empty) `cheat_database_path` for that
launch, and write the game's cheat file, removed again after RetroArch exits.
A game that starts through a NextUI pak gets `Cheats/<TAG>/<ROM file>.cht` on
the card instead (minarch reads it; nothing in `retroarch.cfg` changes), also
removed after the game; a file of the user's own there wins.

`gameAssist` (Settings → Voice & AI → **Game assistant**, on by default) tells
Juka which library game is being played, or was in the last 30 minutes, and on
a TrimUI makes **Select + R2** in a game pause it and open the voice orb over
it. `gameAssistScreen` (**Juka can see the game screen**, off by default; a
TrimUI with `gameAssist` on) sends the paused game's picture, read from
`/dev/fb0`, with the question to Groq's vision model. `gameAssistSpeak`
(**Read answers aloud in games**, on by default; a TrimUI with `gameAssist`
on) speaks the answer over the paused game whatever `jukaVoice` says
([Voice](VOICE.md#asking-about-the-game-youre-playing)).

`vkVoiceAuto` (Settings → Voice & AI → **Start voice typing when the keyboard
opens**, off by default) starts voice typing as a Chat message's keyboard
opens; password, key, address and search fields never listen
([Voice](VOICE.md#voice-typing)).

`autoStart` (Settings → General → **Start JukaHub at login**) writes exactly
one launcher and removes exactly that file again. It is off by default on a
desktop. On a TrimUI the first start switches it on once when nobody has set it
yet (the first-run tour's *Start with your device* step says so and can turn it
off); `autoStartDefaulted` records that this happened, so turning it off is
final and a value set beforehand is never overridden:

| Platform | Hook file |
|----------|-----------|
| TrimUI / CrossMix | `<SD>/System/starts/jukahub-autostart.sh` |
| Linux, macOS | `~/.config/autostart/jukahub.desktop` |
| Windows | `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\JukaHub.cmd` |

The handheld hook is a `#!/bin/sh` script that never holds the firmware's boot
up and is as safe sourced as it is run. It starts JukaHub the way the stock
menu starts an app: it queues JukaHub in `/tmp/cmd_to_run.sh`. With
`autoStartSkipMenu` on (Settings → General → **Skip the stock menu at boot**,
the default since 2026-11-18) it also puts a one-boot stand-in over
`/usr/trimui/bin/MainUI`, so the firmware's loop runs the queue instead of
opening the stock menu; the switch writes or removes
`Apps/JukaHub/.autostart-keep-menu`, which the hook reads. With it off, once the
stock launcher (MainUI) has been up for two seconds and has stopped writing
(eight seconds at most), the hook closes it, so the
firmware's launcher loop runs `launch.sh` with its own environment and brings
MainUI back when JukaHub exits (a loop that reads the queue first starts
JukaHub before MainUI ever shows; when the menu did show, the queued command
puts black on the screen first and the boot fades in from it, instead of the
menu's last picture staying frozen). It starts JukaHub at most once per boot,
steps aside when JukaHub is already running, when another app (a CrossMix
resume, an app picked in the first seconds) is queued, or when MainUI does not
appear within two minutes, and only falls back to running `launch.sh` itself
(with the launcher loop paused by process id and recorded in
`/tmp/jukahub-paused`, which `launch.sh` resumes when JukaHub exits) when the
loop ignores the queue. A start that
hangs leaves `Apps/JukaHub/.autostart-pending` behind (it goes when JukaHub
exits or has been up for 15 s), and the next boot then stays on the stock menu
once. Each boot is logged to
`Apps/JukaHub/autostart.log`, and a hook written by an older build is replaced
the next time JukaHub starts. Once JukaHub has drawn Home the log gets an
indented `boot times` line, in seconds since the kernel started: when the
firmware's launcher script (`runtrimui.sh`) started, this hook's start and
return, when the loop reached the menu's place (the stand-in ran there), when
the queued command started `launch.sh`, and JukaHub's own start and first frame
(from its `/tmp/jukahub-boot-timing`); the gaps are the stock boot's share. Once
per log it also copies the firmware's launcher script, its `/etc/init.d` start
order and the card's mount line (`boot setup:`), for a report. `System/starts`
is the earliest place the stock boot runs anything from the card without the
device's own storage being changed: it runs once the card is mounted, and
`.tmp_update` (MinUI, NextUI, TortOS 1.x) only works after those systems have
replaced `runtrimui.sh` on the device. Other files in `System/starts` are never touched,
and if the SD card cannot be located the toggle stays off and says why rather
than writing somewhere arbitrary.

`startupAnimation` (Settings → General → **Startup animation**) chooses how a
start is greeted: `power` (the default) plays the full boot only when the
device starts into JukaHub and the quick hello otherwise, `always` plays the
full boot on every start, and `quick` never does. A start counts as a power-on
when the autostart hook's `.autostart-pending` marker is there, when it comes
within the first minutes of the kernel's uptime on a TrimUI with `autoStart`
on and the hook's queued command has started JukaHub in this boot (it leaves
`/tmp/jukahub-autostart.ran`), or when `JUKAHUB_BOOT=1` or `--boot` asks for
it (the Windows and Linux login launchers pass `--boot`; Recovery's restart
drops both) — never when JukaHub has already started since the machine
booted, which the kernel's boot id recorded with each first frame
(`bootId` in `startup-state.json`) tells even across a clock change. The firmware boot logo (Settings → General → **Boot
logo**) keeps its record in `Apps/JukaHub/data/bootlogo/state.json`, not in
`jukauser.json`; see [BOOT.md](BOOT.md).

`bootStyle` (Settings → General → **Boot animation**) chooses what the logo
does in that boot: `cinematic` (the default: it crashes onto a cracking floor,
with a buzz of the vibration motor and a knock in the chime), `roll` (it rolls
in on its rounded corners), `image` (the logo still), `classic` (the glass
light running round the pane), `random` (cinematic, roll or classic, a
different one each start) or `none` (no boot animation and no quick hello: the
fastest start, `startupAnimation` then has no effect). Reduce motion switched
on by hand, and battery saver, play `cinematic` and `roll` as `image`; an app
launch plays a short version of the style. See [BOOT.md](BOOT.md).

The startup lock (Settings → Privacy & data → **Startup lock**) keeps four
custom keys. `startupLockSecret` is the salted PBKDF2-SHA256 hash,
`jhlock1$<kind>$<iterations>$<salt>$<key>` with kind `code` (a button code) or
`password`; empty or unreadable means no lock. Set it to `""` on a computer to
remove a forgotten code. `startupLockWhen` is `power` (power-on starts only; the
default on the handheld, detected even with the Quick startup animation),
`start` (every start; the default on a PC) or `wake` (every start and after
sleep). `startupLockFails` counts wrong tries in a row (5 free, then 30 s
doubling to 5 minutes, re-imposed after a restart) and `startupLockPending` (bool)
records a lock that was up and never unlocked, so the next start locks whatever
`startupLockWhen` says. All four are bookkeeping: never exported, never logged.
Voice unlock adds three more: `startupLockVoice` is the spoken phrase's hash,
`jhvoice1$<engine>$<words>$<iterations>$<salt>$<key>` (engine `offline` or
`groq`, PBKDF2-SHA256 over the folded words; empty means no phrase),
`startupLockVoiceMode` is `either` (the phrase or the code), `both` (the code,
then the phrase) or `off`, and `startupLockVoiceMisses` counts misses in a row
(5 rest the phrase until the right code). Removing `startupLockSecret` turns
voice unlock off with it. Bookkeeping too: a change needs the code.

JukaHub OS keeps its state outside `jukauser.json`, in `Apps/JukaHub/os/`:
`os.json` (`enabled` — launch.sh starts `JukaHub --os-session` when it reads
`"enabled": true` —, `layers` mapping a layer id to on/off, `dropCaches`,
default true), `guard.json` (the OS guard's count of failed starts with layers)
and `layers/<id>/layer.json` (the layer format, see
[JUKAHUB_OS.md](JUKAHUB_OS.md)). A running session writes `/tmp/jukaos/`
(`session.json`, `journal.json`, `stock-once`), which a reboot clears.

Quick Settings (`Ctrl+Q`) writes these keys:

| Key | Meaning |
|-----|---------|
| `volume` (0–100) + `volumeSet` | mixer level; written only after you move the slider |
| `brightness` (0–100) + `brightnessSet` | screen brightness, same rule; the header's brightness card writes it too |
| `screensaverTimeout` | idle seconds before the screensaver overlay, cycled by the panel and by Settings → Display → Lock screen after |
| `lowPower`, `reducedMotion`, `fullscreen` | the remaining switches |
| `ledSync` | audio-reactive stick/ring LEDs |

The `*Set` flags separate "never touched" from a real 0, so a fresh install
never overrides the device's own mixer or backlight on first launch. A level is
only restored at startup when its flag is set. The brightness is restored only
where JukaHub is its sole owner — the software dimmer and a plain Linux
backlight: on a TrimUI the firmware keeps its own level (`"brightness"`, 0–10,
in `/mnt/UDISK/system.json`, which every change made in JukaHub also updates),
and on Windows the system keeps it, so neither is overwritten at launch.

Radio switches are **not** replayed at startup: airplane mode is applied when
you press it, not on every boot, and turning it off restores the Wi-Fi/Bluetooth
state that was active before it was enabled.

Each row picks its backend at runtime from a fixed list: `nmcli` then `rfkill`
for Wi-Fi, `bluetoothctl` then `rfkill` for Bluetooth, `rfkill` for airplane
mode, `pactl` then `amixer` for volume. Brightness goes through the brightness
controller (`player/brightnessctl.go`), shared with the header's sun button: the
Allwinner display driver (`/dev/disp`) on a TrimUI, `/sys/class/backlight`
(only direct children of that directory are ever written, and only the
`brightness` file inside them), WMI or DDC/CI on Windows, and otherwise a
software dimmer that darkens JukaHub's own picture. A row with no backend
renders dimmed with the reason, and pressing it says so instead of pretending
to work.

Dock and ambient settings are plain custom keys, documented in
[Dock & ambient](AMBIENT.md): `ambientMode` (bool, default true) chooses
between the ambient idle screen and the starfield screensaver, `ambientWeather`
(default true) shows the dock forecast, and `ambientPhotos` points the slideshow
at a folder.

Video snaps in the Games Library are two custom keys too, documented in
[Games Library → Video snaps](GAME_LIBRARY.md#video-snaps): `gameSnaps` (bool,
default true) switches the preview on or off, and `gameSnapsDir` (string) points
it at a clip collection outside `data/game-snaps/`. `gameSnaps` is written by
Settings → General → **Video snaps**, whose row shows `On`/`Off` from this key
plus how many of the library's games have a clip (`On · 12`, or `On · none` on a
card with no clips); `gameSnapsDir` has no row, so it is edited in
`jukauser.json` or set by the builder.

`networkScanAuthorized` (bool) records the one-time answer to the scanning
prompt on the three network tools (`naabu`, `httpx`, `dnsx`). Absent or `false`
means the first run of any of them asks again; see
[Toolbox → Network scanning](TOOLBOX.md#network-scanning).

`cookieRetentionDays` (int, 0–3650) is how long a saved browser cookie is kept,
written by Settings → General → **Cookies**. 0 — the shipped value — keeps
the session until the Browser's Forget chip drops it; 7, 14, 30 and 90 are the
steps the row offers, and any other number is honoured as written. The policy is
applied when the session is restored at launch and again whenever the setting
changes; see [Browser → Dropping them after a while](BROWSER.md#dropping-them-after-a-while).
`cookieRetentionLabel` is the same value as display text and is rewritten with
it; `cookieRowLabel` is the row's whole value (`30d · since Oct 12`), derived
from the jar's oldest first-seen stamp and re-seeded on every launch and page
entry, so editing it by hand changes nothing. The row's neighbour, **Forget
now**, writes nothing: it drops the saved
session and the file immediately, exactly as the Browser's Forget chip does.

`browserSearchEngine` (string) is the engine the Browser searches with:
`duckduckgo` (the default, and what an unknown value means), `startpage`,
`google`, `bing`, `brave`, `yahoo`, `mojeek`, `wikipedia`, or `custom`, which uses
`browserSearchCustom` (string): a results address with `%s` where the words go
(one ending in `=` takes them at the end, and a bare host gains `https://`). A
`custom` engine without a usable address searches DuckDuckGo Lite.
`browserHomePage` (string) is the page **Home** and a newly opened Browser
show; empty or absent follows the engine's start page, and a value that is not
a web address is ignored the same way. `browserHomeTyped` (string) is the last
address typed for it, kept so the row can still offer it after another choice.
Settings → **Browser** writes all four (a typed value only when it is usable);
see [Browser → Search engine and home page](BROWSER.md#search-engine-and-home-page).

`browserJavaScript` (bool, default true) lets the Browser run page scripts.
Off, every page is drawn as its server sent it; `false`, `0`, `"false"`,
`"0"`, `"no"` and `"off"` all mean off, and a missing key means on. Settings → Browser
→ **JavaScript** writes it as `"true"`/`"false"`; a hand edit applies from the
next page load. See [Browser → JavaScript](BROWSER.md#javascript).

`browserBlockAds` (bool, default true) is Settings → Browser → **Block ads and
trackers**: `false`, `0`, `"false"`, `"0"`, `"no"` and `"off"` let every page
load all the subresources it asks for; anything else keeps the filter on. Sites
excepted one by one (Site info → **Allow ads on this site**, or the Filter
chip) are not Custom keys: they live in `data/browser-block.json`. See
[Browser → Ads and trackers](BROWSER.md#ads-and-trackers).

`browserFullScreen` (bool, default true) draws the Browser's page edge to edge,
with every action in the Select menu instead of the address bar, toolbar,
chip row and shared bars; `false` brings that classic layout back. `false`,
`0`, `"false"`, `"0"`, `"no"` and `"off"` all mean off, and a missing key
means on. Settings → Browser → **Full-screen pages** and the menu's own row
write it as `"true"`/`"false"` and rebuild the screen at once; a hand edit
applies from the next launch or config reload. A `Browser` scene a config
ships itself keeps the layout it was written with. See
[Browser → Full screen and the Select menu](BROWSER.md#full-screen-and-the-select-menu).

Time-lapse recording is two custom keys, documented in
[Time-lapse](TIMELAPSE.md): `timelapse` (bool, default true) films every game
launch, and `timelapse_interval` is the seconds between stills (default 20,
clamped to 3–300).

`ledSync` is opt-in and writes only inside `/sys/class/leds`: LEDs exposing
`multi_intensity` or `color`, plus brightness-only LEDs whose names look like
stick or ring lights. The colour comes from the RMS level and zero-crossing
frequency of the PCM the video and music path is already handing to SDL (the
`mic` mode below is the only one that listens), and the audio thread is never
blocked by the lights. The TrimUI stick rings are not in the LED class: they
are driven through `/sys/class/led_anim`: a colour goes through `frame_hex`, or
straight into `effect_rgb_hex_lr` with the `static` effect, when a read-back
proves the driver takes it that way; otherwise the brightness (`max_scale_lr`
or `max_scale`) dips to 0, the effect is re-armed in the dark and the
brightness comes back. The level is always the scale node, and what the rings
showed before is put back when the sync stops or a game takes the screen.
Frames are quantised to 32 steps and unchanged frames are skipped, so the loop
runs at 25 fps without hammering sysfs. Stopping the sync, or quitting the app,
blanks the LEDs instead of leaving the last frame lit. With no addressable LED
the row is dimmed and the toggle refuses with the reason.

The LED policy is the custom key `led_policy` — `audio` (the default), `mic`,
`battery`, `charging`, `temperature` or `custom` — cycled by **LED Mode** in
Quick Settings. `mic` lights the sticks from the microphone: the level is the
RMS above a noise floor that follows the room, times `led_mic_sensitivity`
(`low`, `normal` (default) or `high`), coloured by pitch like `audio`. The
microphone is open only while the lights are on in this mode and JukaHub is on
screen, and a voice feature (voice orb, Voice command, voice typing) takes it
over until it is done; nothing is recorded. Settings → Stick lights → **Lights show** →
**Microphone** and **Mic sensitivity** write both keys. Battery runs red → green across 10–80%, charging breathes green
and goes dark unplugged, and temperature runs green → red across 45–80 °C (read
from the thermal zone, cached for two seconds). Custom uses `led_color`
(`"#rrggbb"`) and an optional `led_effect` of `solid` (default), `smooth` (a slow
six-second glow between about half and full brightness, eased so it never looks
like a blink; Charging breathes on the same curve), `pulse` (two-second
breathing) or `rainbow` (a full hue turn every twelve seconds). On the TrimUI
stick rings JukaHub checks every 10 s whether the driver still reports what it
reported after JukaHub's own write; a real outside change (TrimUI's service on
charge or wake) is painted over once, softly, and after two such changes in a
session JukaHub leaves the rings alone until the lights are switched off and
on. The log's `led` scene names the driver state when the rings are taken and
the reason for every colour written. `audio` keeps a soft glow with nothing
playing.

`autoPerformance` (bool, default `true`) is Settings → **Automatic CPU speed**
on the handheld: the CPU clock ceiling follows the app in front (lower on Home
and the menus, higher for games, video, the Synth and HTML5 games), steps with
how busy the CPU is and remembers each app's level in `auto-performance.json`.
A game's own CPU profile or limit wins; with `powerProfiles` (Full speed while
playing) off it only ever lowers the clock.

`memorySwap` (bool, default `true` on TrimUI handhelds with 1.5 GB of RAM or
less, `false` with more) is Settings → Games & emulators → **Extra memory
(swap)**: two minutes after start, with no game on screen, JukaHub turns on
compressed swap in RAM (zram, half the RAM up to 512 MB) when the kernel has
it, otherwise a 256 MB `jukahub.swap` in its data folder (512 MB with more than
16 GB free; only with its size plus 1 GB free) at swappiness 10. Nothing is
added while any swap is already on. A card whose file system refuses a swap
file is remembered in `memswap.json` (with the kernel and file system) and not
written again until the switch goes off and on. Off switches JukaHub's own
swap off, deletes the file and puts the swappiness back (with less memory
free than the swap holds it stays on until the next start). See
[LOCAL_AI.md](LOCAL_AI.md) for how the AI's memory check counts it.

`xboxBoost` (bool, default `true`) is Settings → Games & emulators → **Xbox
boost** on TrimUI handhelds: for an Xbox game (hakuX) or a GreenOvercast stream,
the launch runs at full CPU speed even with `powerProfiles` off (a
`powerProfileMaxFreq` cap and the game's own CPU options still apply) with
Automatic CPU speed standing aside, parked cores come online, a devfreq GPU
runs at full speed, JukaHub frees its caches and idle helpers, the game's
process group runs at nice −5, "Hey Juka" stays closed in the game, and a
stream gets Wi-Fi power saving off. All of it is put back when the game exits;
see [DEVICE_TUNING.md](DEVICE_TUNING.md#xbox-boost). Where the handheld plays
GameCube and Wii games (`gamecube` below), they get the same boost.

`gamecube` (`"auto"`/`"on"`/`"off"`, default `"auto"`) is Settings → Games &
emulators → **GameCube & Wii** on TrimUI handhelds: whether GameCube and Wii
games play here, in RetroArch's Dolphin core. `auto` (**Automatic**) plays them
on the Smart Pro S (A523) and not on the A133P models, `on` (**On**, or **Try
anyway** on an A133P) plays them on any model, `off` never (JukaHub then sets
nothing up for them; a Dolphin core installed by hand still starts them). While
they play,
the first start of a game sets the core and Dolphin's system files up, and
JukaHub keeps `dolphin.json` in its data folder: the core file it last installed
or checked (so a build from before the October 2026 freeze fix is replaced
once) and the video driver it wrote into Dolphin's RetroArch override. See
[GAME_LIBRARY.md](GAME_LIBRARY.md#gamecube-and-wii-on-the-handheld).

`led_brightness` (int, 0–100, default 100) trims every policy's colour, so it
dims audio, battery, charging, temperature and custom alike; 0 is an off LED. It
is applied to the RGB value rather than to the sysfs `brightness` file, because
brightness-only LEDs derive their level from the peak channel.

`led_juka` (string, default `always`) is Settings → Stick lights → **React to
Juka**: while the voice orb or the in-game assistant is up the sticks show Juka
over whatever `led_policy` shows — cyan following your voice while it listens, a
slow violet breath while it thinks, blue following Juka's voice while it answers
(amber when the answer is a problem). `always` lights the sticks for Juka even
with the LED sync off (the loop runs only while Juka is up and the firmware's
ring state is put back after), `on` only while the sync is on and
`led_brightness` is above 0, `off` never; anything else means `always`.
`led_brightness` trims Juka's colour too while the lights are on.

`led_policy` also takes `screen` (Settings → Stick lights → **Screen
colours**): the sticks follow the colours at the left and right edges of the
picture, read from the framebuffer about twelve times a second (two narrow
strips, never a whole frame), in JukaHub and over games. A new colour shows once
it has held for half a second (the TrimUI rings blink on a colour change); the
brightness follows the scene. `led_screen_games` (bool, default `true`) keeps it
on over games; `led_screen_off_systems` (comma-separated console names as the
library spells them) lists consoles whose games keep the firmware's lights —
**For this console** adds or removes the last game's console.

`led_ind_low_battery` (default `true`), `led_ind_charging` and
`led_ind_standby` (default `false`) are the stick-light indicators: a slow red
pulse at 10 % or less while not charging, a green breath while charging, and a
dim glow while the screen is off or the device sleeps — in that order, over any
`led_policy`, also over games, while the lights are on. `led_ind_brightness`
(10–100, default 60) is their brightness; `led_brightness` does not dim them.
The low-battery warning stays on until 13 %, and the indicators and the charger
reading change only after holding for a few seconds, so a wobbling reading does
not flicker the rings.

`display_contrast` (-4…5), `display_saturation` (-5…5), `display_exposure`
(-4…5) and `display_colortemp` (-20…20, negative is warmer), all default 0, are
Settings → Display's picture rows: offsets from the firmware's values of
the display driver's `enhance_contrast`, `enhance_saturation`, `enhance_bright`
and `color_temperature` (`/sys/class/disp/disp/attr`). The firmware's values are
kept in `_display_tune_base` the first time a row moves and written back by
**Reset picture**. The rows always show: where a node is missing JukaHub draws
contrast, exposure and colour temperature over its own frames instead
(`displaytune_soft.go`, JukaHub's screens only), and Saturation stays greyed.
The nodes are written again after a wake.

`uiFont` is Settings → Display → **Font**: empty or `inter` is the bundled Inter;
otherwise a downloaded face's id (`atkinson`, `andika`, `lato`, `firasans`,
`ubuntu`, `poppins`, `kanit`, `comicneue`, `sharetechmono`, `vt323`,
`silkscreen`, `pressstart`, `notosansjp`) or `file:<name>` for a .ttf/.otf in
the data folder's `fonts` folder (TrimUI: `<JukaHub>/data/fonts`; Windows:
`%APPDATA%\JukaHub\fonts`). Nothing is downloaded until a face is picked —
the one exception is Settings → Accessibility → **Legible reading font**
(`a11y_legible_font`), which fetches Atkinson Hyperlegible, or Andika for a
Russian interface, into the same folder by itself (see
[ACCESSIBILITY.md](ACCESSIBILITY.md#legible-reading-font)) — and a face whose
file is missing draws in Inter.

The hotspot (Tools → Hotspot) keeps its name and password in `_hotspot_ssid`
and `_hotspot_pass`; the underscore keeps them out of exported settings. See
[HOTSPOT.md](HOTSPOT.md).

**Quick Settings → LED Style** edits all four keys — mode, effect, colour and
brightness — with the animation running for a live preview, and writes them
through the same debounced user-config save. The panel also carries the **LED
Sync** switch, so turning the lights off does not require leaving it.

LAN Play is `netPlayEnabled` (default true in the shipped config) with a
user-level override in the custom key `netplay_enabled`, written by the **LAN
Play** switch in Quick Settings. The override only exists once the switch is
used, so an older `jukauser.json` never turns the feature off by omission.
The relay used by **Host online** is the custom key `netplay_relay` (`nyc`,
`madrid`, `montreal` or `saopaulo`; default `nyc`), set with ←/→ on that row.

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

### The home screen (`layout: "home"`)

The `Main` scene's `style: "tile"` buttons **are** the home menu cards: each
tile's `text`, `x`, `y`, `width`, `height`, `placeholder` (the card subtitle)
and `icon` customize the card that carries the same name — Continue, Media,
Files, Packages, Apps, Favorites, Browser, Chat, Tools, Emulator, Settings
and Exit. Editing a tile (or hot-reloading the config) moves, resizes, renames
and re-icons the card on the next frame; `trigger` keeps controlling what the
card opens. Coordinates are declared on the 1280×720 design canvas and are
scaled (including the 4:3 handheld profile) like every other scene's
geometry. A tile that names no card is ignored — including a tile with no
`text` at all — and geometry outside the canvas or without a positive size falls
back to the built-in card instead of appearing broken. On this scene `icon` names
the card's glyph exactly (`tools` is the slider glyph, `exit` the power ring); an
unknown name draws the plain ring, and an absent one keeps the card's built-in
glyph. The header cluster (network, weather, clock, update,
notifications) is system chrome re-measured every frame and is not
config-customizable.

### Home arrangements and total conversions

The home screen has six arrangements, chosen with the `homeArrangement`
custom variable. **Settings → Theme & look → Home layout** and the strip at
the top of Home's edit mode ([CONTROLS.md](CONTROLS.md#home-layouts)) offer
five of them: Tabs, Standard, Hero grid, Carousel and Widgets.

| Value | In Settings | What it is |
|---|---|---|
| `tabs` (default when unset; `tab`, `jukaos` and `v2` are read as `tabs`) | Tabs | the Tabs Home of JukaOS v2: a strip of tabs (Recent, Main, Games, Entertainment, Favourites, Collections) in the top bar over a page of tiles — see [HOME_TABS.md](HOME_TABS.md) |
| `custom` (and any unknown value) | Standard | the scene's own tiles decide where the cards are — the arrangement described above |
| `dashboard` | Standard | the built-in composition: the wide Continue card with the smaller cards beside it |
| `grid` | Hero grid | a 4×3 mosaic with the Continue card standing two rows tall as a hero tile and the other ten cards filling the rest |
| `carousel` | Carousel | one row of big cards centred on the focused one and wrapping around, with its title and description under it and a row of position dots; the cards are the tiles of `homeTiles` (below), in order, all at one size |
| `widgets` | Widgets | a grid of square cells, seven across, holding the tiles of `homeTiles`, each at its own size: Home cards, Start-menu entries and info widgets; three rows fit on the screen and the grid scrolls past them |

`dashboard` and `grid` ignore the tiles' `x`/`y`/`width`/`height` (and their
titles and icons) while they are in force, and **never rewrite them**: the
scene's elements are left exactly as authored, so switching back to `custom`
puts the user's arrangement back untouched. That is what makes an arrangement
safe to try. `carousel` and `widgets` leave the scene alone too: what they
show, and what edit mode changes, is `homeTiles`. Standard and the Hero grid
cannot be edited on the device; **Edit Home** (L2 on Home) opened on one of
them switches to Widgets first, and back again when editing ends with nothing
changed.

The Tabs Home keeps its own state in four more custom variables:
`homeTabViews` (`"<tab or folder>=<view>"` pairs split by `;`, views `grid`,
`row`, `list`, `carousel`, `compact`; folder keys `store` and `col:<id>`),
`homeTabsShown` (the tab IDs in order, comma-separated, `-` before a hidden
one; a tab it does not name is shown at the end), `homeTabsRemember` (`true`
by default: open on the last tab; `false` opens on Main) and `homeTabLast` (the
tab to come back to — bookkeeping, left out of the config document). Three
more set its look, each a row in Settings → Theme & look and on the Tabs
Home's arranging sheet (L2): `homeTabsPosition` (`top` by default: the tab
strip sits in the top bar; `bottom` puts it in a bar of its own over the
button hints; only the Tabs Home reads it), `homeDimArt` (`true` by default:
on the Tabs Home and in the Games library every cover but the selected one is
drawn darker and greyer; `false` shows them all in full colour) and
`listEdgeFade` (`true` by default: a long list or grid fades into the page at
an edge with more past it; `false` cuts it off at the edge). Both switches
read `on`, `off`, `1` and `0` too. Its Games
tab reads `homeTabsEmuClean` (Settings → Games & emulators → **Hide emulators
without games**): `auto` (the default) hides consoles with no games and dims a
console whose games have no emulator with an Install badge; `all` lists every
console this device has an emulator for, empty ones as "0 games" (`on`, `off`,
`true` and `false` are read too). Collections and non-game favourites live in
`collections.json` ([Other JSON stores](#other-json-stores)). The keys, tab
IDs and examples are in [HOME_TABS.md](HOME_TABS.md#settings-keys).

An experience pack's `homeLayout` takes the same values, `tabs` included: the
pack switches to the Tabs Home, and its card reads "Home: Tabs" over a miniature
of the tab strip. A pack picks the arrangement, never the tiles: on `carousel`
or `widgets` Home shows the user's own `homeTiles`, on `tabs` the user's own
tabs, favourites and collections.

In **Settings → Theme & look**, Colour and Home layout are independent choices.
Colour presets never move a card. The five Home layouts are Tabs, Standard,
Hero grid, Carousel and Widgets. Built-in combinations that only paired a
palette with a layout are hidden from the gallery; saved IDs still work. A
theme file in the themes folder can still set an arrangement and appear under
Styles:

```json
{"name": "Paper tiles", "arrangement": "grid",
 "background": "#f6f1e7", "buttonColor": "#e8dfcd", "...": "the rest of the colours"}
```

The console-inspired looks (**PS5 Style**, **PS4 Style**, **Switch 2 Style**,
**Switch 2 Style Dark**, **Xbox Style** and the classic consoles) appear under
Styles. They also set the tile corners and the focus ring; PS5 Style
turns the Carousel into the console's welcome hub (a row of icon tiles over
trophy pills and a board of info cards), PS4 Style reshapes the row as a
top-left *shelf* and the Switch 2 looks as the console's home screen (an
edge-scrolling *strip* of square tiles led by your recent games, over a line
of round buttons).
A theme file can use the same optional keys: `carouselStyle`, `tileRadius`,
`focusRing` and `focusRing2`. See [THEMES.md](THEMES.md#styles-and-layouts).

### Home tiles (`homeTiles`)

The Carousel and Widgets arrangements show one ordered list of tiles, the
`homeTiles` custom variable. Home's edit mode writes it after every finished
change (a drop, a resize, a removal, an addition, a reset) and when editing
ends, so it seldom needs writing by hand. The value is a list of entries
joined by `;`. Each entry is a tile reference, optionally followed by `@` and
a size in grid cells written `WxH` (width, a lowercase `x`, height):

```text
continue@3x2;media@4x1;emulator@2x1;packages;apps;favorites;browser;chat;tools;files;settings;w:clock
```

That is the default list, and what **Reset** in edit mode puts back. On the
seven columns it packs into exactly three rows: Continue 3×2 at the top left,
Media 4×1 beside it, Games 2×1, Packages and Apps under Media, and Favorites,
Browser, Chat, Tools, Files, Settings and the Clock widget along the bottom.

| Reference | Tile |
|---|---|
| `continue`, `media`, `emulator`, `packages`, `apps`, `favorites`, `browser`, `chat`, `tools`, `files`, `settings` | a built-in Home card (`emulator` is the Games card) |
| `w:clock`, `w:weather`, `w:battery`, `w:storage`, `w:nowplaying`, `w:calendar`, `w:playtime` | an info widget |
| `w:wifi`, `w:bluetooth`, `w:power` | a quick control: **A** switches Wi-Fi or Bluetooth on or off (the tile shows the network, or how many devices are connected), or opens the power card — Sleep, Restart and Power off on the handheld, Exit on a desktop. Where the radio cannot be switched (Windows, macOS) the tile opens the Wi-Fi or Bluetooth page instead |
| `app:scene:<Scene>` | a screen row of the Start menu, by the scene's `name` (`app:scene:Weather`) |
| `app:do:<Start menu entry>` | an action row of the Start menu — a tool, a device action, a plugin's button — by its label (`app:do:Wi-Fi Networks`) |

The Start menu's "Send to …" rows cannot be tiles. Inside a reference, `%` is
written `%25`, `;` is `%3B` and `@` is `%40`. The **Add** tile edit mode shows
(`edit:add`) is never stored.

The grid's cells are square (160 × 152 design pixels, seven across the
1216-pixel body) and a tile can be any whole number of them: 1 to 7 across and
1 to 3 down. Two have a floor. In edit mode **X** starts resizing the focused
tile and the D-pad grows or shrinks it a cell at a time; a click on its size
tab steps through 1×1, 2×1, 2×2, 3×2, 4×2 and 3×3. An entry without `@` (and a
newly added tile) has the tile's default size:

| Tile | Default | Smallest |
|---|---|---|
| `continue` | 2×2 | 2×2 (shorter, its chip, progress bar and button would collide) |
| `media` | 2×1 | 2×1 (its waveform needs the second column) |
| `w:calendar` | 2×2 | 1×1 |
| `w:nowplaying` | 2×1 | 1×1 |
| every other card, widget and `app:` tile | 1×1 | 1×1 |

A stored size outside those limits is clamped to them (`@9x5` becomes the
largest the grid allows). The carousel draws every tile at the same size, so
sizes only matter on Widgets.

Reading the value is tolerant, so a hand edit never breaks Home:

- a missing or blank `homeTiles` is the default list; `none` is an empty list,
  and Home then shows just the **Add tiles** tile;
- blank entries are skipped, unknown cards are dropped, a reference that
  repeats keeps its first place, and at most 64 tiles are read (edit mode
  stops adding at 64);
- a `w:` widget this version does not know (a typo, or one a newer version
  saved) is kept, drawn dimmed with "Not available here", and keeps its stored
  size, so editing Home here never deletes it;
- an `app:` tile is kept even when its screen or entry does not resolve on
  this device right now: it is drawn dimmed with "Not available here" and
  works again wherever it resolves. The name after `app:scene:` / `app:do:`
  is matched without regard to case.

Tiles are placed in list order, row by row on seven columns: each goes at the
first place after the previous tile where it fits, wrapping to the next row,
and a hole left beside a wide or tall tile is never filled from further down
the list, so the reading order is always the list order.

### Top bar (`headerLayout`)

The order of the controls on the right of every top bar, and which are hidden,
is the `headerLayout` custom variable; Edit Home's **Top bar** row writes it
([CONTROLS](CONTROLS.md#edit-home)). It is a comma-separated list of ids, left
to right; a leading `-` hides that control:

```
brightness,volume,-bluetooth,wifi,weather,clock,battery,media,update,trophies,bug,notifications
```

| Id | Control |
|----|---------|
| `brightness` | the brightness button (sun) |
| `volume` | the volume button (speaker) |
| `bluetooth` | the Bluetooth button |
| `wifi` | the Wi-Fi meter (or the Offline badge) |
| `weather` | today's high / low |
| `clock` | the clock (the calendar button) |
| `battery` | the battery gauge |
| `media` | the now-playing / output chip |
| `update` | the release offer, while one is advertised |
| `trophies` | the trophy room's cup |
| `bug` | Report a bug |
| `notifications` | the notification bell (cannot be hidden) |

Missing or blank is the shipped order above with nothing hidden, and Reset
stores it that way. Ids are read without regard to case; unknown ones and
repeats are skipped, a `-` on `notifications` is ignored, and a control the
list does not name goes back after the one that comes before it in the
shipped order. A control is still only drawn while it has something to show
(no forecast, nothing playing, no release offered), and a crowded bar drops
parts the usual way.

### Quick Settings (`quickSettingsLayout`, `quickSettingsTiles`)

Edit Quick Settings (**L2** in the panel, [CONTROLS](CONTROLS.md#edit-quick-settings))
writes two custom variables:

- `quickSettingsLayout`: `tiles` (the default: a four-column grid), `compact`
  (six columns of icon buttons) or `list` (one row per button). Anything else
  is `tiles`.
- `quickSettingsTiles`: the buttons in order, joined by `;`, each with an
  optional `@WxH` size in grid cells, and a leading `-` on a hidden one:

```
wifi;bluetooth;-airplane;vpn;volume@4x1;brightness@4x1;lowpower;quiet;music
```

| Id | Button | Default size |
|----|--------|--------------|
| `wifi`, `bluetooth`, `airplane`, `vpn` | the radio switches | 1×1 |
| `volume`, `brightness` | the sliders | 2×1 |
| `lowpower`, `quiet`, `music`, `magnifier`, `ledsync`, `motion`, `fullscreen`, `lanplay` | Low Power, Quiet Mode, Background music, Magnifier, LED Sync, Reduced Motion, Fullscreen, LAN Play | 1×1 |
| `powermode`, `lock`, `spatial`, `ledmode`, `experience`, `autooff` | Power Mode, Lock screen, Spatial audio, LED Mode, Experience, Auto power off | 2×1 |
| `networks`, `btdevices`, `ledstyle`, `legend`, `closeall`, `power` | Wi-Fi Networks, Bluetooth Devices, LED Style, Controller Legend, Close all apps & games, Power | 1×1 |

Sizes run from one cell (two across for the sliders) to six across and two
rows; the four-column Tiles grid draws anything wider than four across its
whole width, and the List ignores sizes. Missing or blank is the default
order above with nothing hidden, and Reset stores it that way. Ids are read
without regard to case; unknown ones and repeats are skipped, and a button the
list does not name goes back after the one that comes before it in the default
order. A button this device does not have (Bluetooth Devices without
bluetoothctl, Auto power off without an RTC wake-up) keeps its place in the
list and is simply not drawn.

### Background music (`bgMusic`, `bgMusicSource`, `bgMusicVolume`, `bgMusicSelectedPath`)

Settings → Sound & haptics writes the background-music custom variables.
`bgMusic` is the switch (`"true"` / `"false"`; missing is off). `bgMusicSource` is the music:
`calm`, `lofi` and `chip` are the built-in songs, `synth` plays the Synth's
saved `synth.json` (its demo when there is none) and `library` shuffles the
music library's songs (not videos or audiobooks), falling back to the downloads
music folder. `selected` plays the file or shuffles the folder stored in
`bgMusicSelectedPath`; users choose either through the Files picker in Sound &
haptics, and a folder includes supported audio in its subfolders. An unknown
source is `calm`. `bgMusicVolume` is 0–100 (missing is 40). The music pauses
while a game, the music player, a video, the Synth, Quiet
Mode, the voice orb, dictation or an alarm has the speaker, and while the
screen sleeps. `library` and `selected` need ffmpeg already installed (it is
never downloaded for this) and otherwise play `calm`.

### Notifications during videos (`notifyDuringVideo`)

Settings → Media writes `notifyDuringVideo` (`"true"` / `"false"`; missing is
off). Off, a full-screen video that is playing is not interrupted: toasts that
do not answer a button press (pressed at most 1.5 s before) wait out their time
unseen, still filed in the bell, and when the video pauses, stops or leaves full
screen one line says how many came in. Errors always show. On, those toasts show
over the video, and so do new chat messages (Discord, Juka's answers) as
"Sender: message" toasts. Quiet Mode still holds every toast.

### Glass, Settings lists and screen vision (`glassStyle`, `settingsShowAll`, `jukaSeesScreen`)

- `glassStyle` (`"classic"` default, or `"realistic"`): Settings → Display &
  theme → Glass. See [THEMES.md](THEMES.md#glass-style).
- `settingsShowAll` (bool, default `false`): Settings normally opens each
  section on its most-used rows, with "More settings (N)" for the rest; `true`
  shows every row, the old full lists (Settings → About → Show all settings).
  The Date & time rows are at the end of General and the Keyboard rows at the
  end of Controls; the section ids `datetime` and `keyboard` still resolve.
- `jukaSeesScreen` (`"off"`, `"ask"` default, or `"always"`): Settings → Voice & AI → Let
  Juka see the screen. See [VOICE.md](VOICE.md#asking-about-whats-on-screen).

## `elements`

| Field | Type | Notes |
|-------|------|-------|
| `type` | string | see the table below. An unknown type is logged and skipped |
| `text` | string | label/caption/button text |
| `x`, `y` | number or numeric string | accepts `30` and `"30"` (a `px` suffix is tolerated) |
| `width`, `height` | string or int | accepts `"280"` and `280`; surrounding spaces are ignored, while a `px` suffix, a fraction or a negative size falls back to the element's default |
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
| `icon` | string | glyph name for a tile: a home card's name (`media`, `files`, `tools`, `exit`, …), an explicit tile glyph (`disk`, `cache`, `clock`, `logs`, …), or empty to let the label pick the picture |
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
| `packagelist` | Package Manager list: registry packages on the left, the focused one's details on the right, and a ✓ on the rows already on the card; pressing an installed row offers Reinstall / Remove / Cancel |
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
| `chat_send`, `chat_clear`, `groq_chat`, `discord_connect` | chat and assistant |
| `attach_image`, `send_to_chat` | files for the chat: `attach_image` opens Files to pick files (any kind, despite the name) for the chat tab on screen; `send_to_chat` puts the focused Files row in the community tab's tray and opens the chat — see [Sending files](CONTROLS.md#sending-files) |
| `tick`, `add_ticker`, `clear_tickers` | stock/crypto tickers |
| `netspeed_run`, `benchmark_run`, `benchmark_random`, `hw_load`, `cron_load`, `cron_startup` | network test, benchmarks, hardware info, scheduled tasks and startup items |
| `terminal_run`, `emulator_run`, `external_app` | run a command, an emulator, or an external program |
| `log_export`, `log_clear` | export or clear the app log |
| `cache_clear` | clear the on-disk cache |
| `plugin_action`, `plugin_refresh` | run a plugin action, rescan the plugins directory — see [Plugin API](PLUGINS.md) |
| `packages_refresh` | re-fetch the Package Manager's registry |
| `textbrowser_refresh` | reload the textbrowser from its source |
| `textbrowser_system`, `_zeroconf`, `_json`, `_processes`, `_network`, `_temps`, `_services`, `_files`, `_copy`, `_clear` | textbrowser data sources and clipboard |
| `tools_open`, `processes_open`, `wifi_open`, `ip_open`, `ip_add`, `ip_fetch` | the built-in tool panels (including the [Wi-Fi picker](WIFI.md)) |
| `companion_open` | the phone companion QR card (toggles) |
| `ftp_toggle`, `ftp_start`, `ftp_stop`, `ssh_toggle`, `ssh_start`, `ssh_stop` | FTP and SSH/SFTP servers |
| anything starting with `http://` or `https://` | opens the URL in the system browser |

On the Tasks screen, **Refresh** (`cron_load`) updates both scheduled tasks and
startup items; **Startup** (`cron_startup`) updates only startup items. On
Windows, scheduled tasks come from a read-only `schtasks /query` and only the
first entries fit in the panel; open Windows Task Scheduler for the full list.
Windows startup-item scanning is not supported. On Linux, the viewer reads cron
and systemd information and checks common startup locations.

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

## Other JSON stores

The config is not the only file the player writes. Everything below holds work
the user did in the app, so every one of them is written through `AtomicWrite`
(temp file, `fsync`, rename) and read through `readJSONWithBackup`, which falls
back to the `.bak` the writer leaves beside it. A store that is damaged *and*
backupless is logged as such — none of these start empty without a line in the
log.

| File | Holds |
|------|-------|
| `favorites.json` | favourites from before they moved into `jukauser.json` (older cards) |
| `game-options.json` | per-game overrides: CPU ceiling, core, launch flags |
| `auto-performance.json` | Automatic CPU speed: the clock level each game and screen settled at (safe to delete) |
| `game-recents.json` | the Continue row |
| `game-cheats.json` | Game options → Cheats: the cheat file used per game and the switched-on cheats |
| `artwork-aliases.json` | Game options → Name for covers: the title each game's box art is looked up by, read before the built-in aliases. Hand edits are fine; a file JukaHub cannot read is never overwritten ([Games Library → Choosing a cover by hand](GAME_LIBRARY.md#choosing-a-cover-by-hand)) |
| `cheats-pending.json` | one-launch cheat files not yet removed after RetroArch exited (repair record) |
| `musicfavorites.json` | starred tracks |
| `music-playlists.json` | named playlists saved from the audio player |
| `theme-catalog.json` | cached copy of the community theme index, so the Theme library browses offline |
| `theme-updates.json` | which installed themes came from the gallery and a hash of what was written, so a new gallery version can be told apart from your own edits (safe to delete: installed themes are re-adopted, and any that differ from the gallery count as edited) |
| `audioresume.json` | audiobook/podcast positions and playback speed |
| `game-history.json` | per-game launches, recent sessions and interrupted launches |
| `ipstreams.json` | the IPTV library you added |
| `saverradio.json` | the radio stations the idle screen plays when the card has no music, downloaded from GitHub and refreshed weekly (safe to delete: it is fetched again) |
| `tvfavorites.json` | starred channels |
| `browser-cookies.json` | saved sessions (mode 0600). **A deliberate drop deletes the `.bak` too** — a whole Forget, one site's Forget, or a retention prune; a forgotten session must not survive on the card |
| `browser-bookmarks.json` | bookmarks. Clearing the last one removes the file and its backup |
| `browser-block.json` | sites where ad-blocking is switched off |
| `partyfriends.json` | LAN party friends |
| `partyhistory.json` | watch-time baselines per room |
| `psremote.json` | PS4/5 host list and pairing keys (mode 0600) |
| `startup-state.json` | startup-app bookkeeping |
| `save-sync/folders.json` | the Syncthing folder manifest |
| `curation/profiles/*.json` | profiles built in the Curation hub |
| `power-scripts.json` | Toolbox snippets (mode 0600) |
| `terminal_history` | terminal history (a text file, one command per line) |
| `notes.json`, `jukaalarm.json` | notes and alarms |
| `collections.json` | the Tabs Home's collections and its non-game favourites: `{"version":1,"collections":[{"id","name","items":[{"ref","title"}],"created"}],"favourites":[{"ref","title"}]}`. Refs are `game:<path>`, `sys:<console>`, `app:scene:…`, `app:do:…` or a Home card ID (`col:<id>` only among favourites). At most 100 collections of 500 items, 500 favourites, names up to 40 characters. A damaged file is restored from the `.bak` and kept as `collections.json.corrupt`. Game favourites are not here: they are the Favorite tag in `game-meta/games.json` |

Three files are read but never written by the player, so there is no backup to
fall back to and the loaders say so instead: `game-launchers.json` (per-console
launch paths, written by you or a patch — a broken one falls back to the
built-in catalog), `game-profiles/` catalogs and community imports (a damaged
file is reported as an issue in the panel), and the `port.json`/`plugin.json`/
app `config.json` manifests owned by other software.

Caches are deliberately **not** backed up: `weather_cache`, the
RetroAchievements cache, the artwork-miss store, the patch `db.json` and the
emulator-update `source.json` are rebuilt from their source, so a reset costs a
refetch, not a user's work. `bt-output-volume.json` (the volume the speaker and
each Bluetooth headset, by address, were last left at) has no backup either: a
lost file only means each output starts at the current level once. They are still written atomically where the writer
already did so.

## Privacy and diagnostics

Anonymous diagnostics are controlled by `variables.Custom.telemetryOptIn` in
`jukauser.json` and are **off** unless explicitly enabled in **Tools → Community
& support**. When enabled, every tenth launch sends an informational public
GitHub issue through the JukaHub report service. The issue is titled `[Info]`
and requests the `non-issue` and `from-device` labels. It contains only:

| Field | Example |
|-------|---------|
| `kind` | `anonymous_diagnostics` |
| `version`, `release` | `v1.2.3`, `JukaHub-20261017…` |
| `device`, `platform` | `TrimUI Smart Pro`, `Trimui` |
| `os`, `arch` | `linux`, `arm64` |
| `diagnostics` | CPU core count, scene/element/schema counts, safe-mode state |

No install ID, account, user-authored config content, filename, URL, log or
full diagnostics bundle is sent. The launch count and any pending report stay
on the device; an offline report retries on a later launch. Turning the switch off clears
both. A separately configured telemetry collector (`-X main.telemetryEndpoint=…`
or `JUKAHUB_TELEMETRY_URL`) still receives a one-word `panic` event after a
crash while the switch is on.

**Report a bug** (same panel) sends a report built from `DiagnosticsText`: app
and release version, build date, device, OS/architecture, config scene/element
counts, safe-mode state and backup count. It never includes config values,
tokens, usernames or private paths. Reports go to the JukaHub report service
(`https://generate.jukalang.com/api/bug-report`, which files a public GitHub
issue) unless `JUKAHUB_BUGREPORT_URL`, a `bugReportURL` custom key,
`-X main.bugrepEndpoint=…` or the telemetry collector names another
([BUG_REPORTS.md](BUG_REPORTS.md)).

---

## Related

- [Plugin API](PLUGINS.md) — plugin manifest, entry points and execution contract
- [Controls](CONTROLS.md) — the buttons that drive these elements
- [Building](BUILDING.md) — how the shipped `jukaconfig.json` is produced
