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
  scans and previews them. Only an on-demand "fetch the rest" action from the
  library itself is absent.
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
- **No `NetPlayEnabled` gate.** The flag exists in [config.go](../player/config.go)
  but nothing writes it, so a config that predates this feature would ship the
  panel switched off with no way to switch it back on. It stays a documented
  field; the gate should arrive together with a Settings row that writes it.
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

### 2. In-game OSD, screenshots and recording

JukaHub cannot draw over an emulator: the emulator owns the framebuffer and
JukaHub is blocked waiting for it to exit. An "in-game OSD from the hub" would
never appear. The working design is to configure the emulator's own OSD:

- Write a JukaHub-managed overlay preset and hotkey bindings
  (`input_menu_toggle`, `input_screenshot`, `input_toggle_fast_forward`) into
  `retroarch.cfg`, pointing at a JukaHub-owned overlay directory.
- Set `recording_output_directory` to the card, so RetroArch's own record
  toggle writes a file we can list afterwards; add a "Recordings" row to Media
  that plays them through the existing video player.
- Pin both with a test that reads the cfg writer, not a screenshot.

### 3. Simple mode with a PIN

- A persisted `simpleMode` switch that renders only an allowlist of scenes (Home,
  Media, Games, Settings → Simple) and hides the scene-switching chrome.
- Leaving needs a PIN typed on the on-screen keyboard; the PIN is stored hashed
  with the existing secret store, not in plain text in the config.
- It is a *focus* mode, not security: the SD card can be edited from a PC. Say so
  in the settings row so nobody trusts it as parental control.

### 4. Box art for the rest of the library

The fetch already exists (`startROMArtworkFetch` in `romsetup.go`, driven by the
onboarding ROM setup), so this is a smaller job than it looks:

- Add a **Fetch missing art** action to the Game Library (and behind the
  Continue card's empty tile) that runs the same fetch over the *scanned* list
  rather than the onboarding one.
- Keep a per-title "not found" marker so an unmatched ROM is not retried on
  every scan, and cache under the existing artwork root.
- Textures must be created on the render thread; the download stays on a worker —
  the exact BrickOS failure noted in [IMPROVEMENTS.md](IMPROVEMENTS.md)
  (worker-thread texture creation produced black covers).

### 5. Game switcher with quick resume

- Foundation exists: recents, play time, game profiles, low-battery auto-save.
- The missing piece is state automation: `savestate_auto_save`,
  `savestate_auto_load` and a per-ROM state slot, so "Resume" boots the game with
  the last state already loaded. RetroArch does the work; JukaHub only sets the
  switches and remembers which game is "open".
- A real switcher (suspend one game, start another, come back) needs two emulator
  processes suspended on a 1 GB device. Treat that as experimental and start
  with save-state resume instead.

### 6. LED policies and haptics

- Extend [ledsync.go](../player/ledsync.go) from audio-only to a policy: per
  scene/emulator colour, battery (green→red), charging pulse.
- Haptics exist only if the kernel exposes a rumble device; detect it and hide
  the setting when it does not, rather than showing a dead toggle.

### 7. Controller legend on first launch

- The footer hint registry already knows every binding per scene; a first-run
  overlay can reuse `footerHints` instead of a second hardcoded list, and the
  same overlay belongs behind a `?` chip in Quick Settings for later reference.

### 8. A curated store

- The mechanism is done (signed packages + Patch transactions). What is missing
  is a curated index with compatibility gates (`min_firmware`, device model,
  package dependencies) and a storefront scene: categories, screenshots, install
  state. Reuse `ComponentList` rendering from Patch so the store is not a second
  UI stack.
