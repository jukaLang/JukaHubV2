# Feature backlog: what is shipped, what is a gap, what cannot work

A grounded answer to the "make the hub indispensible" list. Every item below was
checked against the code before being classified, because several of them are
already implemented and a few cannot work the way the list imagines on this
device.

## Shipped in this pass

| Item | What changed | Tests |
|---|---|---|
| Continue card with real play data | `getRecentItems` in [recent.go](../player/recent.go) merges the game recents (`game-recents.json`, already written by every library launch) with media recents into one timeline. Games relaunch through the same launcher resolution as the library (`resolveGameLauncher` in [game_library.go](../player/game_library.go)), report a missing ROM or emulator as a toast, and name the console on the tile. | `continue_games_test.go` |
| Offline weather | [weather_cache.go](../player/weather_cache.go) writes the last good forecast after each fetch and restores it before the first network attempt, so an offline boot shows weather instead of "unavailable". The popover header says `Saved 3 hours ago` when the data is from the cache; anything older than a week, future-stamped, or unparseable is discarded. | `weather_cache_test.go` |
| LAN play (link cable over Wi-Fi) | [netplay.go](../player/netplay.go) + [netplay_ui.go](../player/netplay_ui.go): **X** on a library game opens Host / Join. Hosting resolves RetroArch and a netplay core, prints this device's `ip:port` before launching, and starts `retroarch -L <core> <rom> --host --port N --nick X`; joining takes `ip[:port]` on the on-screen keyboard and starts `--connect`. Both sides need the same core and ROM, and the panel says so. | `netplay_test.go`, `netplay_ui_test.go` |
| Save-state resume with auto-save | [savestate_resume.go](../player/savestate_resume.go) turns on RetroArch's `savestate_auto_save` and `savestate_auto_load` for a launch when the resolved launcher actually runs RetroArch. The edit is line-based, keeps a one-time `retroarch.cfg.jukahub-backup`, and never touches a standalone emulator. `savestateResume: false` opts out. | `savestate_resume_test.go` |
| Fetch missing box art from the library | **L1** / **F** in the Games Library rescans and downloads covers for games without a local PNG, reusing the onboarding downloader. Recent 404s are cached in `game-art/.missing-art.json` for 30 days; worker-thread textures are reloaded on the render thread when the fetch finishes. | `artwork_fetch_test.go` |
| Recordings screen for emulator captures | [recordings.go](../player/recordings.go) points RetroArch's `recording_output_directory` at a folder the hub knows (or honours the folder the user already set), through the same crash-safe config writer the save-state switches use, then lists the clips newest first. **Apps → Recordings** plays one with A in the existing video player (the Media button row has no room left: the IPStream chip is injected into it at runtime). `recordingsDir` renames the folder, `recordings: false` opts out. | `recordings_test.go` |
| Library completeness and resumable fill-the-gaps | The library header draws a cover-completeness ring, each console row a meter, and the status line names the count. The fetch reports progress per game, can be stopped with the same action, and resumes from the files already on the card; the counts are recounted from disk after each run. | `game_library_test.go`, `artwork_fetch_test.go` |

## Already there (the list assumed these were missing)

- **LAN watch party + PIN-paired companion.** `partynet.go` runs a real HTTP
  room server on the LAN with invite codes; `companion.go` pairs a phone by QR /
  PIN and drives the same session.
- **RetroAchievements (softcore).** `retroachievements.go` writes the
  `retroarch.cfg` switches, stores credentials with the app's secret store, and
  shows progress. Hardcore is deliberately not enabled.
- **A package list with verification.** `packages.go` + `patch_repo.go` install
  signed packages from a repository through the transactional Patch engine
  (backup, rollback, digest and signature checks). What is missing is curation,
  not the mechanism.
- **Per-ROM box art, including the fetch.** `romsetup.go` downloads missing
  covers from libretro-thumbnails during ROM setup, and `game_library.go`
  scans and previews them. An on-demand "fetch the rest" action now ships from
  the library itself (see the table above).
- **Screenshots.** `screenshot.go` captures the SDL frame from anywhere in the
  hub (not inside an emulator — see the OSD section).
- **LED sync.** `ledsync.go` already mirrors music to the device LEDs through
  `/sys/class/leds`.

## Gaps, with the honest design

### 1. Wi-Fi netplay (shipped, RetroArch only)

The nxredux model — link-cable play over a network — is exactly what RetroArch's
netplay does, and it works across platforms (the Brick, an Android phone, a PC)
because it is the same protocol and the same core. It now ships as the **LAN
Play** panel in the Game Library (`X` on a game); see
[GAME_LIBRARY.md](GAME_LIBRARY.md#lan-play-link-cable-over-wi-fi).

What the implementation decided differently from the sketch above, and why:

- **No `retroarch.cfg` writes.** `--port` and `--nick` are real command-line
  flags, so the port and nickname are pinned per launch and the user's config
  is left untouched. Public announce and the relay stay at whatever the user
  chose; nothing is announced for a LAN session started from the hub.
- **`NetPlayEnabled` now has a switch.** Quick Settings' **LAN Play** row
  writes the flag (custom key `netplay_enabled` in `jukauser.json`, so a config
  that predates the feature keeps the shipped default, on), and the panel
  refuses to open with a toast pointing at the row. The gate lives in
  [netplay_ui.go](../player/netplay_ui.go); the shipped default is in
  [jukaconfig.json](../player/jukaconfig.json).
- **No QR yet.** A QR would have to encode an address the other device still
  has to type into RetroArch's own netplay menu, so the panel prints the
  address in full and remembers the last one instead. A QR that only pre-fills
  JukaHub's own field is still worth adding.
- **Standalone emulators remain out of reach.** Drastic, PPSSPP and melonDS each
  have their own incompatible link emulation, so a generic "Wi-Fi link cable"
  for them cannot be built from outside; the panel refuses PSP and DS with that
  reason rather than launching a session that cannot connect.

Still needs two machines on one network to verify end to end. The command
builder, address parsing, core mapping and both preflights are unit-tested.

### 2. In-game OSD, screenshots and recording (recording shipped)

JukaHub cannot draw over an emulator: the emulator owns the framebuffer and
JukaHub is blocked waiting for it to exit. An "in-game OSD from the hub" would
never appear. The working design is to configure the emulator's own OSD:

- ✅ Set `recording_output_directory` to the card, so RetroArch's own record
  toggle writes a file the hub can list; the **Recordings** screen plays them
  through the existing video player. See
  [RECORDINGS.md](RECORDINGS.md). Pinned by `recordings_test.go`, which reads the
  cfg writer instead of taking a screenshot.
- Still open: a JukaHub-managed overlay preset and the hotkey bindings
  (`input_menu_toggle`, `input_screenshot`, `input_toggle_fast_forward`).
  Deliberately not written yet — the binds are numeric button codes, and a
  guess breaks controls on a device whose mapping nobody measured. The overlay
  also needs art the repo does not ship. What is left is assets plus a profile
  per device, not more plumbing.

### 3. Simple mode with a PIN

- A persisted `simpleMode` switch that renders only an allowlist of scenes (Home,
  Media, Games, Settings → Simple) and hides the scene-switching chrome.
- Leaving needs a PIN typed on the on-screen keyboard; the PIN is stored hashed
  with the existing secret store, not in plain text in the config.
- It is a *focus* mode, not security: the SD card can be edited from a PC. Say so
  in the settings row so nobody trusts it as parental control.

### 4. Box art for the rest of the library (shipped)

The library now has the missing-art action (**L1** / **F**), the per-title
not-found cache, the render-thread texture reload, the completeness ring/meters
and a stop/resume path for the download. What is still open is the quality of
the match, not the plumbing: titles the libretro set spells differently
(`Zelda II` vs `Zelda II - The Adventure of Link`) are recorded as not found. A
manual "choose image" action and a title-normalisation table are the next step;
neither needs a new downloader.

Descriptions, genres, release dates and screenshots are a separate source
problem: libretro only publishes images, so that data needs a provider with an
account or key (ScreenScraper, TheGamesDB) behind the same downloader.

### 5. Game switcher with quick resume (first half shipped)

- Foundation exists: recents, play time, game profiles, low-battery auto-save.
- **Resume is now wired:** a RetroArch launch enables `savestate_auto_save` and
  `savestate_auto_load`, so quitting writes an auto state and the next launch
  loads it. See [GAME_LIBRARY.md](GAME_LIBRARY.md#resume-and-auto-save).
- A real switcher (suspend one game, start another, come back) still needs two
  emulator processes suspended on a 1 GB device, and named per-game save slots
  need core-specific state handling. Treat those as experimental. The design
  options, hard constraints and a phased plan are in
  [CHECKPOINTS.md](CHECKPOINTS.md); nothing there is implemented yet.

### 6. LED policies and haptics (haptics shipped)

- Extend [ledsync.go](../player/ledsync.go) from audio-only to a policy: per
  scene/emulator colour, battery (green→red), charging pulse.
- Haptics are in [haptics.go](../player/haptics.go): navigation, confirm, back,
  toggle and toast pulses drive the TrimUI's vendor GPIO motor (SDL rumble for
  an external pad), and the Vibration switch is only injected into Settings →
  General when a motor was found. Left: an intensity step, and per-emulator
  rumble (a core runs in another process, so that needs a hook the launchers
  can call).

### 7. Controller legend on first launch (shipped)

[controlslegend.go](../player/controlslegend.go) adds the card: it appears once
when the setup tour is completed (a skipped tour does not interject), a
"Controller Legend" row in Quick Settings reopens it any time, and B closes it.
There is no second hardcoded list — section one is `footerHints` output for the
scene it opened over; section two lists only the three global pad facts (Start
menu, Select + Start quit, F1 → the keyboard shortcuts reference). Tests:
`controlslegend_test.go` (content mirrors the footer, the Quick Settings row
replacement, the tour reveal, and an offscreen render).

### 8. A curated store

- The mechanism is done (signed packages + Patch transactions). What is missing
  is a curated index with compatibility gates (`min_firmware`, device model,
  package dependencies) and a storefront scene: categories, screenshots, install
  state. Reuse `ComponentList` rendering from Patch so the store is not a second
  UI stack.
