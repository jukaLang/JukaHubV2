# Feature slate, ranked

For current progress and chosen improvement priorities, see
[PROJECT_STATUS.md](PROJECT_STATUS.md). This page contains unadopted ideas only;
its rankings are not an implementation schedule.

Two slates are ranked here. The first (time machine, ghost runs, Library Royale,
AI hints) is the 36-idea list; the second, further down, is the dashboard /
platform / ecosystem list (widgets, radio, sync, AI configuration).
Every entry was checked against the code first: what already exists decides the
cost far more than how cool the idea is.

**This page holds unadopted ideas only.** An idea that ships, or that is accepted
into [player/MODERNIZATION.md](../player/MODERNIZATION.md) or
[ROADMAP.md](../ROADMAP.md), leaves this page — what already shipped out of these
slates is inventoried once in
[MODERNIZATION §5.5](../player/MODERNIZATION.md#55-shipped-feature-surface-verified-2026-10-25),
with the code and the test that pins it, so no idea needs two registers.

## How this was ranked

- **Cost** is engineering time for a slice that is worth showing, with tests:
  **S** ≈ under a day, **M** ≈ 2–4 days, **L** ≈ 1–2 weeks, **XL** ≈ weeks.
- **Fit** is how much of it the tree already carries: save snapshots, the ffmpeg
  pipeline, RA config writing, LAN rooms, the phone companion, the package
  repo, playtime/achievements, themes and the plugin contract.
- **Risk** is the chance the device cannot do it at all. Two constraints drive
  most of the ranking:
  1. **An emulator is a black box.** JukaHub launches it detached and is blocked
     waiting for it to exit, so it cannot draw over the game, cannot read its
     RAM, and cannot capture its frames from our own renderer. Anything that
     needs per-frame game data has to go through RetroArch's config/flags or the
     device framebuffer (`/dev/fb0`, already referenced in
     [patch_swap.go](../player/patch_swap.go)).
  2. **The handheld has no camera and no second display.** QR ideas have to be
     mediated by the Phone Companion ([companion.go](../player/companion.go)),
     not by pointing the device at something.
- Anything already shipped is not repeated: the shipped register is
  [MODERNIZATION §5.5](../player/MODERNIZATION.md#55-shipped-feature-surface-verified-2026-10-25),
  and [FEATURE-BACKLOG.md](FEATURE-BACKLOG.md) holds the designed gaps.

## Tier 1 — build next

### 1. Save timeline: the honest half of the "time machine" (M)

The slate imagines a continuous buffer of screen + save RAM + input for the
last hour. Screen and input are not reachable: the emulator owns the
framebuffer and its input. **Saves are reachable**, and that is the part that
actually hurts when it is lost.

Already in the tree: [savemanager.go](../player/savemanager.go) snapshots every
save root into a timestamped folder with a manifest and restores them;
[savestate_resume.go](../player/savestate_resume.go) turns on RetroArch's
auto-save/auto-load states.

First slice: snapshot the save roots every N minutes *while a game runs* (the
shipped time-lapse recorder in [timelapse.go](../player/timelapse.go) already
knows when a launch starts and ends), plus a timeline list in Save Manager with
"restore this moment". No new format, no emulator cooperation.

Proof needed: a real RetroArch session on the card, and that a snapshot taken
during play is restorable after the emulator exits.

### 2. OS achievements + daily quests (S)

[playtime.go](../player/playtime.go) already counts per-scene and per-day time
and unlocks badges; `funpack_test.go` pins the behaviour. Add an unlock tree
over the things the hub knows (emulators used, systems played, themes tried,
packages installed) and derive three daily quests from the same data. Cosmetics
as rewards: themes and boot sounds already exist.

### 3. Library Royale (M)

A meta-game over the collection: random game, random challenge, permadeath
across games. Pure metadata plus timers — no emulator introspection needed if
the game's outcome is the player's word plus the clock, which is enough for a
bar game. Cheapest genuinely original idea on the slate; [playtime.go](../player/playtime.go)
and the recents list supply the history it needs.

### 4. QR/companion sharing of the things the hub owns (S–M)

Profiles, themes, save snapshots, high scores and configs can be exported today
as files ([savemanager_ui.go](../player/savemanager_ui.go) already zips a
snapshot for transfer). The companion uploads files from a phone
(`companion.go`), the package repo verifies signed content, and
[curation.go](../player/curation.go) defines portable profiles. Add export →
QR/pairing code → import on the other device, with the signature check the
package engine already has.

Not "bump two handhelds together" — no camera — but the same social result.

## Tier 2 — worth building, with a decision first

| Idea | Cost | What exists | Decision to make |
|---|---|---|---|
| Time capsule / letters | S | Notes + config custom data + Playtime dates | Nothing structural; just a UX for sealed notes. Do it when there is room for delight work. |
| Save-file forensics & diff | M | Snapshots, restore, file listing | Hex/diff is generic; "known offsets" is per-game curation. Ship the generic half. |
| Ghost runs | L | RA launch flags, LAN rooms | RA-only (input replay needs the core), and determinism is core-dependent. Decide whether RA-only is acceptable. |
| Randomizer suite | L | Patch transactions, signed packages, QR sharing | No IPS/BPS applier in the tree yet; the tracker needs per-game item maps. Start with the patch applier. |
| Speedrun toolkit | M | Playtime clock, LAN, packages | Timer/splits/LiveSplit import are self-contained; auto-split needs memory reads we cannot do. Upload needs API keys. |
| Arcade kiosk mode | M | Ambient, screensaver, cast, themes | Marquee on a second screen is not a device capability; cast to a phone is. |
| JukaHub TV | M | Live TV scene, shorts, library, cast | "Broadcast" over your own library means launching games unattended. Decide whether it is a playlist of clips instead. |
| Retro OS skins | M | Theme presets, `design.go`, boot/exit screens | A full Win95 shell is L; a skin pack with window chrome is the slice. |
| Secret modes | S | Controller chords, config | Cheap and fun; no blockers. |
| Reactive music visualiser | S | `backgroundfx.go`, `dynamicbg.go`, decoder PCM | Needs an FFT over decoded audio; the decoder already produces PCM. |
| Virtual pet / garden | M | Home layout, playtime data, themes | Art and state machine only; sticky, but pure product work. |
| JukaHub Radio | S–M | Music player, PEQ, cast, station presets | Licensing for game soundtracks; local-library shuffle is free. |
| Chiptune tracker / pixel editor | L | Juka language, canvas sandbox, package repo | Two editors and a publish path; strong differentiator, not a quick win. |
| Playable credits | S | JukaLand, arcade | Content, not technology. |
| LAN arcade leaderboard | S–M | `partynet.go` HTTP rooms | Only JukaHub's own arcade games expose scores; emulator scores need maps. Start with the arcade. |
| Async board games over QR | M | QR generation, companion | Needs a board-game engine; QR size limits mean file/mailbox transfer, not one code per move. |
| Museum | M | Scenes, cards, gallery | Content-heavy; no emulator demo ROMs may ship. |
| OS-level quests beyond #2 | S | playtime, recents | Merge into #2 rather than a second system. |

## Tier 3 — expensive, niche, or needs a partner

| Idea | Cost | Why it is here |
|---|---|---|
| ROM autopsy suite | L | Per-system disassemblers, tile and palette formats, map viewers. Great for a preservation audience, small on a handheld. |
| Manuals + OCR | L | Needs an OCR engine that is not bundled; PDF viewing already works. Start with manual PDFs per game and full-text search. |
| Developer commentary | M | Real content and licensing per game; the sync mechanism is easy. |
| Receipt-printer trophies | M | USB thermal printer support, ESC/POS, hardware to test. Fantastic demo, hardware-bound. |
| 3D-printable trophies | S | STL generation is trivial; payoff is off-device. |
| Campfire / presence | M | LAN presence exists, but chat and avatars are a social product, not a feature. |
| Personal streaming server | XL | Streaming *JukaHub* is shipped (cast); streaming an emulator would need framebuffer capture plus input injection. The shipped time-lapse recorder (`timelapse.go`) already captures the panel, so treat this as that stack plus a long road. |
| Link-cable / hardware bridge | XL | Needs physical adapters and per-system protocols. |
| AI dungeon master | M | Text-only; needs an API key path. Fine, but not device-specific. |
| Digital game lending | L | Legally awkward (copying ROMs), and time-limited tokens are DRM we would have to own. Lend saves/profiles instead. |

## Does not work as imagined

- **Live translation overlay for import games.** It needs OCR of the emulator's
  frames *and* drawing over them. JukaHub can do neither while the emulator owns
  the screen. A RetroArch overlay shows static images, not translated text.
  Closest honest version: a companion-phone translation view of a captured
  frame, which is a worse experience than pointing a phone at the screen.
- **Stuck-in-a-game hint system with screen awareness.** Same reason: no frame,
  no RAM. It can still work as *hint packs* keyed by game title with
  progressive reveal (nudge → hint → spoiler), which needs no AI and no screen.
- **Generic ghost runs and auto-split.** Both need input/RAM access the hub does
  not have. RetroArch can carry ghosts; standalone emulators cannot.
- **Second-screen marquee / HDMI.** There is no second-display path in the
  tree; the phone cast is the only "another screen" that exists.
- **Coin economy from achievements** is fine, but arcade coins cannot gate
  emulator launches without becoming an annoyance; scope it to the arcade.

## Cheap wins, under a day each

Secret modes (Konami on the pad), daily quests, time capsules, "playable
credits", JukaHub Radio from the local library, a reactive visualiser (the player
already draws synthetic sine bars — this would be the real FFT), and the first
half of save forensics (list + diff two snapshots).

## If only three shipped next

The old "if only four" list named auto time-lapse, which has since shipped
(MODERNIZATION §5.5). What is left of it:

1. **Save timeline** — solves the loss that actually happens, reuses snapshots.
2. **OS achievements + quests** — cheap retention over data already collected.
3. **Library Royale** — turns the collection into a new game.

Ghost runs and AI hints are the exciting ones; both are capped by the emulator
being a black box, so they belong after the three above, not before.

---

# Second slate: dashboard, platform and ecosystem

The dashboard list reverses the first one's economy. The first slate leaned on
infrastructure that already existed (save snapshots, ffmpeg, LAN rooms, the
companion); almost none of this one does. The checks below are greps for the
feature itself, not guesses:

| Claim | Reality in the tree |
|---|---|
| Widget system | No runtime. [homelayout.go](../player/homelayout.go) draws a fixed set of cards from code. |
| Widgets authored as JS/Lua/WASM | No host. [plugins.go](../player/plugins.go) runs `python3` or `sh` with `--key=value` arguments and returns text; nothing renders UI from a plugin. |
| Builder as layout editor | The builder is an HTTP receiver ([livebuilder.go](../player/livebuilder.go)) for config edits. There is no WebSocket transport and no preview of a widget tree. |
| Theme-from-image | Theme presets are compiled colours in a Go map ([themes.go](../player/themes.go)); user themes already load from a `themes/` folder next to the app ([themeloader.go](../player/themeloader.go)). There is no palette extraction from an image. |
| Save sync | [savemanager.go](../player/savemanager.go) snapshots, restores and writes a Syncthing folder list. No sync agent, no WebDAV, no conflict UI. |
| Radio / podcasts / scrobbling | The music player decodes local files through ffmpeg; no station directory, RSS, Last.fm or ListenBrainz. |
| AI configuration | No LLM client anywhere. The builder and [config_schema.go](../player/config_schema.go) can validate a config, which is the half that matters. |
| Behavioural events | No webhook/IFTTT emitter; no Discord RPC client. |

## Already shipped

The fourteen-row table that used to sit here listed ideas that were already in
the tree. Naming them again here would be a second register, so they are
inventoried once, with code evidence, in
[MODERNIZATION §5.5](../player/MODERNIZATION.md#55-shipped-feature-surface-verified-2026-10-25)
instead of being tracked here as well.

## The genuinely new work

| Idea | Cost | Note |
|---|---|---|
| **Theme-from-image** | M on top of the shipped store | Needs median-cut/quantisation to the 16 roles and the picker; purely local, no key. |
| **Save-sync conflict UI** | M (agent L) | Snapshots + Syncthing export exist; add a merged save index and a keep-device / keep-cloud / keep-both card. A real WebDAV agent is a second project. |
| Internet radio | L | ffmpeg reads URLs, so playback is the easy half; a station directory, buffering state and metadata are the work. |
| Podcasts | M (shares radio) | RSS parse, subscribe, offline cache, resume, speed. |
| Scrobbling (Last.fm/ListenBrainz) | S | Needs accounts and keys; the play-event source already exists in the music player. |
| Widget foundation | M (first slice), XL platform | A persisted layout plus built-in widgets is tractable. Community authoring needs a rendering contract. |
| Community widget runtime | XL | The honest contract is a sandboxed JSON UI descriptor executed by the hub, not arbitrary JS/Lua/WASM; arbitrary code in-process would put the SDL loop and the secret store at risk. |
| Builder as widget editor | L | Depends on the descriptor contract; the HTTP transport exists, WebSocket does not. |
| AI config assist | M + key | The schema and validation exist; the LLM call and a consent/opt-in path do not. |
| Docs search over the config reference | M | Embeddings need a key or a bundled index; a local index over `docs/` is possible and cheaper. |
| Webhook/event bus | S | "Game launched", "achievement unlocked", "install finished" as HTTP POST. Unlocks IFTTT and Discord without writing either client. |
| Discord Rich Presence | S, desktop only | Needs the desktop Discord IPC socket; on the handheld there is nothing to talk to. Ship it for Windows builds or skip it. |
| RSS headlines | S (with widgets), M alone | A scene without a widget surface; trivial with one. |
| Netplay lobby / discovery | M | LAN play and its panel exist ([netplay.go](../player/netplay.go)); UDP broadcast, room codes and a QR invite are the missing parts. |
| Random game picker / daily challenge | S | Library + recents + playtime; pure product work. |
| Bezel/shader packs | M | RetroArch owns shaders and overlays; the package engine can install them, the hub cannot render them. |
| Icon / sound packs | M | Package category plus a loader; design tokens and `sound.go` already centralise the look and the beeps. |
| Seasonal themes by date | S | The time-of-day hook exists; add a date table and a package category. |
| Playtime limits / break reminders | S | Playtime ticks already drive the toast pipeline; launch blocking is a small gate. |
| Blue-light / bedtime mode | S | No gamma control on this panel; a warm theme plus the existing brightness slider is the honest version. |
| Pomodoro blocks launching | S | The Pomodoro scene exists; the block is a check in the launch paths. |
| Juka playground | M | JukaLand already runs Juka; add an editor/tutorial surface and publish through the package repo. |
| PICO-8-style fantasy console | XL | Sprite/sound editors plus a runtime contract; a platform project. |
| ROM verifier (CRC32/SHA1 vs DAT) | S–M | Pure Go hashing; the DATs are the cost (size, licensing) and belong in packages. |
| Cheat database (.cht) | M | Parse libretro cheats and hand RetroArch the file at launch; per-game toggle. |
| Save-state slots | M | State files exist; thumbnails do not (panel capture is not available while the emulator runs), so slots are metadata-only. |
| Per-game controller remap | M | RetroArch per-content remap files plus an `--appendconfig`; standalone emulators are out. |
| Input display | S for the hub, blocked for games | The hub can draw its own input overlay; it cannot overlay the emulator. |
| Clip recording (last 30 s) | XL for games, S for hub video | A ring buffer of emulator frames needs continuous capture; the hub's own player can clip its decoder output. |
| Screenshot gallery | S | Group the BMPs `screenshot.go` writes by game and show them in the image viewer. |
| Kodi remote | S | JSON-RPC over HTTP; the LAN client pattern already exists. |
| Plex/Jellyfin client | M | HTTP API, auth, library shelf, play through the existing decoder. |
| Steam Deck pairing | L | New protocol and a second app to test against. |
| Home Assistant kiosk dashboard | M | The HA controls exist; a full dashboard is a web view plus auth handling. |
| QR Wi-Fi provisioning | S–M | The companion already pairs by QR; carrying credentials needs care with screenshots and logs. |
| Voice search via phone mic | M | The phone page can capture audio; recognition needs a service or an on-device model. |

## Cannot work as imagined

- **Couch/TV mode auto-detected on HDMI.** There is no second-display path anywhere
  in the tree, and the device is a handheld panel. The phone cast is the only
  "another screen" that exists (and it is already shipped).
- **Phone as a second screen showing maps or inventory.** That is game RAM, and
  the hub cannot read it. RetroArch netplay shares frames, not memory.
- **Streaming overlay with input display and achievement toasts.** Same black box:
  nothing can be drawn over the emulator.
- **"Last 30 seconds" clip of emulator play.** A shadow-play buffer needs
  continuous encoding of the framebuffer on a 1 GB device; that is not a toggle,
  it is the most expensive item on this list.
- **Widgets as arbitrary JS/Lua/WASM.** A UI runtime that draws from third-party
  code has to be a sandboxed descriptor interpreter; anything else hands the
  render loop and the secret store to a package.

## Revised four, in build order

Playtime Wrapped was the first pick on this list; it has since been adopted into
the plan ([MODERNIZATION §5.6](../player/MODERNIZATION.md#56-adopted-not-started)),
so it is not repeated here. What is left, in order:

1. **Theme-from-image (M)** — the user theme store already ships
   (`themes/*.json`), so this is palette extraction against the 16 roles plus
   the picker that applies the result.
2. **Save-sync conflict UI (M)** — the export half is shipped; the conflict card
   is what makes people trust it. Syncthing is the target to test against.
3. **Radio + podcasts (L)** — the daily-use expansion, and the only one of the
   four that needs new content plumbing as well as UI.
4. **Widget foundation (M first slice)** — persisted layout, built-in widgets
   (Now Playing, Continue, clock/weather, battery/disk). No community runtime
   until a descriptor contract is written and reviewed.

Cheap wins to slot between them: daily challenge mode (S) — the random game
picker itself already ships in `game_random.go` — webhook event bus (S), seasonal
theme by date (S), screenshot gallery (S), Kodi remote (S).

The ranking above is idea-only now: Wrapped is in the plan, and the slates above
hold what has not been adopted.
