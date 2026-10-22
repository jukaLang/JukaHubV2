# Controls

JukaHub is controller-first: every screen is reachable with the D-pad and the
four face buttons. Keyboard and mouse are supported on desktop builds.

The top bar always shows the system status — network, weather, clock, battery
and the notification bell on the right, the **Apps** pill and a **back** pill on
the left. The bottom of every screen is the hint bar for real controller
bindings; there is no second clock or battery indicator down there.

The header chips are buttons on every screen: the weather chip
(`21C / 12C`, or `69F / 53F` in the US) opens **Local weather**, a seven-day
forecast for the detected location; the time opens the calendar. The chip uses
Fahrenheit automatically in the US and its territories, and Celsius elsewhere,
until a unit is set explicitly in Settings. Both chips can be tapped or reached
with the D-pad — Up from the top-right cards lands on the weather chip, then
the clock, and **Right** from the clock lands on the notification bell. **A** on
the bell opens the notification centre, exactly like tapping it; **B**/Esc
closes it.
**Local weather** is modal: **A**/**B**/Esc closes it, a tap outside dismisses
it, and the D-pad does not move the screen underneath while it is open.

All bindings below are also listed in-app: press **F1** anywhere for the
shortcuts overlay, **B** or **F1** to close it.

---

## Everywhere

| Button | Action |
|--------|--------|
| D-pad / left stick | Move the selection |
| A | Open / confirm |
| B | Back (header pill, `Escape` and the scene's Back action all do the same) |
| X | Secondary action — delete, toggle, search, depending on the screen |
| Y | Tertiary action — new/create, save, submit in the chat keyboard |
| L1 / R1 | Previous / next page of the focused list, or a −10/+10 s skip in the player |
| L2 / R2 | Bigger jump: previous / next page (fetching the next page of Media results), previous / next video in the player, first / last row in a list, previous / next section on Home |
| Start | Open the start menu (pause) |
| Back | Settings / menu |

Shoulders are contextual, not scene-specific: whatever list holds the focus
owns them. **L1/R1** step one window and **L2/R2** jump further, so the pad
behaves the same in a menu as it does in a media grid.

| Screen | L1 / R1 | L2 / R2 |
|--------|---------|---------|
| Media results, Shorts grid, Apps grid | One screenful | Previous / next results page (R2 fetches it) |
| Favourites, packages, channel / file lists | One window of rows | First / last row |
| Text browser | Scroll a page | Top / bottom |
| Wallpaper picker | Five wallpapers | First / last wallpaper |
| Image viewer | Previous / next image | Jump ten images back / forward |
| Notification centre, clipboard history, global search, social notifications | One window of rows | First / last row |
| Quick Settings | Five rows, or ±5 on a slider | First / last row |
| Start menu (Apps) | One window of apps | First / last app |
| Home | — | Previous / next section |

## Home

| Button | Action |
|--------|--------|
| A | Open the focused tile |
| B | Settings |
| Y | Toggle theme (cycles the presets) |
| X | Favourites |
| D-pad | Move between tiles, the header chips and the footer row |
| Right (from the clock) | Move onto the notification bell; **A** opens the notification centre |

## Media

| Button | Action |
|--------|--------|
| X | Search YouTube |
| Y | Shorts feed: opens Shorts, pre-loads it and plays the first clip, then keeps going after each one ends |
| L1 / R1 | Previous / next screenful of the loaded results |
| L2 / R2 | Previous / next results page; **R2** fetches the next page when it has not been loaded |
| B | Back |

The **Next page** chip appears in the bottom-right corner of the grid once the
focused card is on the last loaded row. Clicking it, pressing **R2**, or pressing
Down on the last card all load the next page of the same query and focus its
first result, so a search keeps going instead of stopping after the first batch.

## Live TV

Playlists (`.m3u` / `.m3u8`) live in the `tv` folder next to the app, or use
**Load Playlist** to fetch the public iptv-org list (cached for six hours).
Channels are grouped by playlist category, so a long file does not have to be
scrolled channel by channel.

| Button | Action |
|--------|--------|
| Left / Right | Previous / next category (**All**, **Favorites**, then the groups) |
| Up / Down | Move through the channel list |
| A | Play the highlighted channel |
| X | Search channels (filters while you type) |
| Y | Add / remove the channel from favourites |

Favourites are stored in `data/tvfavorites.json` next to the app, so they
survive playlist reloads and updates.

## Browser

A text web browser for GitHub issues, release notes and documentation. See
[docs/BROWSER.md](BROWSER.md) for setup and limitations.

| Button | Action |
|--------|--------|
| Up / Down | Scroll the page; Up at the top and Down at the bottom hand the focus to the header and the button row |
| L1 / R1 | Scroll by a page |
| Left / Right | Walk the links on the page, scrolling them into view |
| A | Open the highlighted link, or edit the address bar when no link is picked |
| Y | Search the web (opens the keyboard); the **SEARCH** chip does the same |
| X | Open the full link list for the page |
| B | Leave the browser |

Links are walked in reading order and outlined as they come into focus, so a
link deep in an article can be followed without opening the picker. **Y** (or
the **SEARCH** chip beside the address bar) opens the keyboard on an empty
field: type a phrase to search, or a host to open it. The **Back**, **Forward**,
**Reload**, **Links**, **Search**, **Home** and **Issues** chips at the bottom
are ordinary scene buttons, reachable by pressing Down past the end of the page
and Up to come back.

## Video player

The control bar is a focus ring: **Left / Right** walk it from stop through
rewind, play/pause, fast-forward, speed, subtitles, settings and full screen, and
**A** presses the highlighted button.

| Button | Action |
|--------|--------|
| Left / Right | Move the selection along the control bar |
| A | Press the selected control (play/pause when nothing is selected) |
| Up / Down | Volume |
| X | Subtitles on / off |
| Y | Video settings sheet |
| L1 / R1 | Skip −10 / +10 seconds |
| L2 / R2 | Previous / next video in the list that launched this one (search results or the Shorts feed) |
| B | Close the settings sheet, then stop and go back |

The rewind and fast-forward buttons skip 10 seconds per press. With the settings
sheet open, **Up / Down** move between its rows and **A** applies the row, so
subtitles, full screen and speed are reachable without a touch screen. A tap on
a control also moves the ring there, so touch and D-pad never disagree.

Progress, resume position and subtitles are handled automatically; the control
bar hides itself while playback continues and reappears on any input. When a
video reaches its end the player closes and returns to the scene — the frozen
last frame no longer keeps the grid behind it locked.

## On-screen keyboard

Opens for search fields, the chat composer, the terminal and the unit converter.

| Button | Action |
|--------|--------|
| D-pad | Move between keys |
| A | Type the selected key |
| L1 | Shift (upper/lower case), without losing the focused key |
| Y | Submit (send in chat, search elsewhere) |
| B / Back / Start | Close |

It also closes with its own close button, by tapping outside the panel, or when
you press **Done** / **Send**.

## First-run tour

The first launch opens a short, skippable tour of the D-pad controls, top-bar
clock and notification bell, Appearance settings, and the Start menu. **A** or
**Enter** advances, **Left** goes back, and **B** or **Esc** skips.

Replay it any time from **Settings → General → Replay tour**.

## Game profiles

Open **Start → Game Profiles**, or the pinned **Game Profiles** row in
**Emulators**. The panel launches Windows games through Box64/Wine, native
binaries and scripts. Profiles are JSON files in
`data/game-profiles/`; see [GAME_PROFILES.md](GAME_PROFILES.md) for the format
and the import/export workflow.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through profiles |
| A | Launch the selected profile |
| X | Export it to `export/<id>.json` |
| Y | Rescan `profiles.json`, `import/` and `local/` |
| B | Back (to Emulators when opened there) |

On a keyboard, **Enter**/**Space** launches, **X** exports, **Y** or **R**
rescans, and **Esc**/**B** goes back. Tapping a row launches it; tapping outside
closes the panel.

## Windows apps

Open **Start → Windows Apps**, or the pinned **Windows Apps** row in
**Emulators**. The panel lists discovered `.exe` files and launches basic
Windows programs through Wine. On TrimUI's no-X11 firmware it can use
WestonPack's DRM/KMS compositor; see [WINDOWS_APPS.md](WINDOWS_APPS.md).

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through apps |
| A | Launch the selected app |
| X / H | Show the runtime setup page |
| Y / R | Rescan the app folders |
| B | Close setup, then the launcher |

Tapping a row launches it; tapping outside closes the panel.

## PortMaster ports

Open **Start → PortMaster Ports**, or the pinned **PortMaster Ports** row in
**Emulators**. On a fresh card, **A** installs PortMaster's official TrimUI
package into `Apps/PortMaster`. Afterwards the panel lists PortMaster first,
then every port launcher in `Ports/`, `Roms/Ports`, `Roms/PORTS` and
`Data/ports`. See [PORTMASTER.md](PORTMASTER.md) for paths and limitations.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through PortMaster and installed ports |
| A | Install PortMaster, open it, or launch the selected port |
| B | Back (to Emulators when opened there) |

Tapping a row activates it; tapping outside closes the panel.

## Save manager

Open **Start → Save Manager**. The panel snapshots emulator save folders,
restores a snapshot, prepares the folders for Syncthing and zips the newest
snapshot for a phone. See [SAVE_MANAGER.md](SAVE_MANAGER.md) for the folders and
the snapshot layout.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through the rows |
| L1 / R1 | Page by five rows |
| A | Snapshot, open, or restore — press again to confirm a restore |
| X | Delete the selected snapshot (press again to confirm) |
| B / Esc | Back from the snapshot list, then close |

A restore always takes a safety snapshot first. Tapping a row activates it;
tapping outside closes the panel.

## Curation Hub

Open **Start → Curation Hub**. The hub has an essentials checklist, a theme and
text-size profile exchange, and a compatibility vote list. See
[CURATION.md](CURATION.md) for the bundle contents and the feed format.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through rows |
| L1 / R1 | Page by five rows |
| A | Open, apply, capture or toggle a vote |
| X | Delete a saved profile (Profiles page) |
| Y | Export the focused profile, or submit votes |
| B / Esc | Back one page, then close |

Tapping a row activates it; tapping outside closes the panel.

## Games

Open **Start → Games**. Snake, Tetris, 2048, Pong, Minesweeper and Flappy all
use the D-pad; **A** selects or performs the primary action and **B** leaves a
running game. **R** restarts a lost round.

| Game | Special controls |
|------|------------------|
| Minesweeper | **A** reveal · **X** / **Y** flag · arrows move |
| Flappy | **A** / **Space** flap · **R** restart |

## Playtime & achievements

Open **Tools → Playtime & Achievements** for total time, the last seven days,
the most-used screens, and the badge list. Stats are stored in the same user
config as other settings and survive restarts. Badges unlock automatically for
milestones such as one hour total, 30 minutes of games, 30 minutes of music, and
eight different screens.

### RetroAchievements

On a TrimUI Smart Pro, open **Start → RetroAchievements**,
**Emulators → RetroAchievements**, or open **Playtime & Achievements** and press
**Y**. The screen shows recent unlocks and completion progress for the account
already configured on the card.

JukaHub reads the username from Spruce or RetroArch and the Web API key from
`Saves/cheevos/apikey.txt` (or Spruce's saved `webApiKey`). It never edits those
files. If nothing is configured, use **X** → **A** to enter a username and a
32-character key from `retroachievements.org/settings → Keys`.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through recent unlocks |
| A | Edit the selected account field (setup screen) |
| X or Start | Account settings |
| Y | Refresh from RetroAchievements |
| B | Back / close |

The last successful sync is cached on the SD card, so the screen still shows
your history when Wi-Fi is unavailable. A key entered in JukaHub is stored only
in its data folder and is never displayed in full.

## Music player

| Button | Action |
|--------|--------|
| Up / Down | Move through the playlist |
| A | Play the highlighted track |
| X | Play / pause |
| Y | Favorite the playing track |
| Left / Right | Seek −10 s / +10 s |
| L1 / R1 | Previous / next track |
| L3 (left stick click) | Show / hide synced `.lrc` lyrics |
| R3 (right stick click) | Cycle the sleep timer (off, 15, 30, 60 min) |
| Start | Toggle the full-screen visualizer |
| Guide (logo button, where the pad reports it) | Toggle shuffle |
| Select | Turn the screen off (playback continues) |
| X, then D-pad Right (within 0.45 s) | Open song search |
| Y, then D-pad Left (within 0.45 s) | Toggle background playback |
| B | Close the visualizer, then go back |

### Search

Press **X**, then **D-pad Right**, and type the initials of the title: `iwyn`
finds *I Want You Now*. **R1** (or **A**) plays the highlighted hit, the D-pad
moves the selection, and **B** closes the panel. The search box opens the
on-screen keyboard, so it works without a physical keyboard.

### Screen off (the lock screen)

**Select** darkens the panel while the decoder keeps running — the player's
lock screen. What the controls do while it is dark is chosen in
**Menu → Audio → Screen-off controls**: *Any key* wakes on the first press,
*Controls* keeps transport and volume working, and *Select only* ignores
everything except the wake button.

Volume keys are the exception in every mode: they never wake the panel, because
they belong to the mixer. A **short press** changes the volume, and a **long
press skips track** — next on hold Vol+, previous on hold Vol− — even with the
screen dark and the player left behind. The hardware buttons are read from the
kernel input devices (`/dev/input/event*`), so this works on the handheld where
the OS owns the volume keys and SDL never sees them; `-`/`+` and
VOLUMEUP/VOLUMEDOWN keysyms go through the same hold logic on a keyboard.

### Equaliser

**G** (or **Start → Audio** for the settings) opens the 31-band PEQ: the ISO
one-third-octave centres from 20 Hz to 20 kHz, ±12 dB per band.

| Input | Action |
|-------|--------|
| Left / Right | Pick a band |
| Up / Down | ±1 dB on the selected band |
| Y | Cycle presets (Flat, Bass, Vocal, Rock, Night) |
| X | Flatten the selected band |
| G / B | Close the panel |

Gains are saved to the config as `musicPEQGains`, and the curve restarts the
decoder immediately so the change is audible. **Bit-perfect DAC output** (in
**Menu → Audio**) turns the equaliser and the tempo filter off and hands the
decoded samples straight to the device; the two cannot be on at the same time.

**Shuffle**, **Repeat** and **Fav** also sit in the transport row and are tapped
directly on the touchscreen, which is the reliable path on a handheld whose pad
does not report a guide button.

On a keyboard, **Space** pauses, **L** toggles lyrics, **[** / **]** change the
listening speed, **T** cycles the sleep timer, **E** cycles the auto-exit timer,
**V** opens the full-screen visualizer, **M** toggles shuffle, **R** cycles
repeat off/all/one, **F** favorites the playing track, **Q** queues the
highlighted row to play next, **/** opens search, **G** opens the equaliser,
**O** turns the screen off, **K** opens **Menu → Audio**, and **-** / **+** set
the volume. Holding either volume key still skips a track instead of repeating
the volume step.

Shuffle builds an order that starts with the current track and visits every
track once before wrapping, so it never repeats a song early. Repeat one
restarts the track and repeat all wraps at the end of the list or shuffle walk.
The play-next queue wins over the folder order and is shown as `N queued` in
the status line. Favorites are stored in `musicfavorites.json` in the app data
folder, survive updates, and are marked with an accent dot between the row
number and the title.

### Layouts and the audio settings

**Menu → Audio** (or **K** in the player) opens the audio settings:

| Row | Values |
|-----|--------|
| Now Playing layout | Auralis Pro (artwork card) or StockOS (compact list-first) |
| Cover art opacity | 10% – 100%, applied to the artwork tile and its spectrum |
| Screen-off controls | Any key / Controls / Select only |
| Background playback | On keeps the music running when you leave the player |
| Bit-perfect DAC output | Bypasses the PEQ and the tempo filter |
| 31-band PEQ | On / off; the curve is edited with **G** in the player |
| Keyboard shortcuts | Opens the shortcuts reference |

Both layouts share the same hitboxes, so touch, keyboard and pad work the same
in either one. The choice and the opacity live in the config (`musicNowPlayingLayout`,
`musicArtOpacity`) and survive updates.

A `.lrc` file next to the track (same file name) is loaded automatically; its
timed lines appear inside the artwork square and the current line is
highlighted. Long files — audiobooks and podcasts of eight minutes or more —
resume where you stopped, and the listening speed is remembered between
launches.

## Wi-Fi picker

Open it from **Quick Settings → Wi-Fi Networks**, **Hardware → Wi-Fi**, or a
`wifi_open` button. The radio switch itself stays in Quick Settings.

| Input | Action |
|-------|--------|
| Up / Down | Move through networks |
| A | Join the highlighted network (a saved one reconnects without a password) |
| X / `d` | Disconnect, keeping the saved network |
| Y / `r` | Rescan |
| SELECT / `f` | Forget the saved network — press twice to confirm |
| B / Esc | Close the picker |

A new secured network opens the on-screen keyboard for its password. The field
is masked, and **A** on the keyboard's key submits; closing the keyboard cancels.

## Power tools

Open **Tools → Power tools** for the developer and power-user panel. The
landing page holds six tools; **A** opens one, **B** goes back, and the list
scrolls when the font is large.

| Input | Action |
|-------|--------|
| Up / Down | Move through rows (the output pane scrolls line by line) |
| A | Open a tool, run the highlighted probe or snippet, or go back from output |
| X | Delete a saved snippet or restore a config snapshot (press again to confirm); in output, toggle the hex view |
| Y | Save the clipboard as a snippet; in output, pretty-print JSON |
| L1 / R1 | Page through a list, or scroll output by ten lines |
| B / Esc | Back one level; closes the panel from the landing page |

**Network probes** scan 13 common TCP ports on the guessed gateway, or on a
host copied to the clipboard (**Scan clipboard host**), ping
`1.1.1.1` (the platform's ICMP ping, falling back to a TCP connect when no
ping binary exists), run the installed `traceroute`/`tracert`, resolve two host
names and read the Wi-Fi link quality from `/proc/net/wireless` (or `netsh` on
Windows).

**File inspector** only opens a fixed list: `jukauser.json`, `jukaconfig.json`,
`power-scripts.json`, `power-bench.json`, `logs.txt`, `errors.txt` and
`crash.log`. JSON opens pretty-printed, logs open at the newest lines, and
binary content opens as a hex dump. Nothing in the config can add a path.

**Script snippets** ships five read-only commands and runs them through the
platform shell with a 30-second timeout. Copy a command to the clipboard and
press **Y** to save it as a button. Snippets run with JukaHub's permissions:
only save commands you trust. Saved snippets live in `power-scripts.json`.

**Folder sync** runs `rsync -az --itemize-changes` directly, never through a
shell, and never adds `--delete`. Set the source and target by copying a path
and pressing **A** on the row. The Syncthing row only checks the local web UI
at `127.0.0.1:8384`; it does not need an API key.

**Config history** keeps the 20 newest `jukauser.json` snapshots in
`power-config-history/` next to the config. A snapshot is taken before each
save when the content changed; **A** shows a compact line diff against the
current file and **X** restores it (twice, to confirm). Restart JukaHub after a
restore so the in-memory design is rebuilt.

**Benchmark board** runs a 32 MiB write/read test, records the device model and
MB/s in `power-bench.json`, and sorts by the slower of read and write. The
**Same model** row counts entries for this device model and shows its best
score. Set `powerBenchURL` in the config (or the `JUKAHUB_BENCH_URL`
environment variable) to a JSON array of the same entries to fetch an online
board; **A** on **Copy board** puts the local and fetched entries on the
clipboard.

## Community & support

Open **Tools → Community & support** for the changelog, a prefilled GitHub bug
report and the anonymous-diagnostics switch. The **What's new** page also opens
by itself once on the first launch after an update.

| Input | Action |
|-------|--------|
| Up / Down | Move through the rows, or scroll the changelog |
| Enter / A | Open the changelog, report a bug, toggle diagnostics, or go back from the notes |
| B / Esc | Close the panel |

**Report a bug** writes a diagnostics file, copies the redacted report and
opens a GitHub issue with a summary, reproduction steps and diagnostics already
filled in. **Anonymous diagnostics** is off by default; when on, it sends only
the build, device model, platform, OS/architecture and panic counts — never
designs, filenames, links or account details.

## Watch Party

Host on one handheld and join from the others on the same WiFi. The host runs a
small HTTP room on port 8318 (or the next free port); the invite is the address
plus a five-character code. Anyone in the room can queue a link or press
play, pause and seek for everyone, and the panel shows presence, the shared
queue and a watch-time leaderboard.

| Input | Action |
|-------|--------|
| F8, or Tools → Watch Party | Open / close the panel |
| Up / Down | Move through the rows |
| Enter / A | Host, join, play a queued item, copy the invite or leave |
| X / Delete | Remove the highlighted queue item |
| B / Esc | Close the panel (the party keeps running) |

**Join** opens the on-screen keyboard for an invite such as
`192.168.1.5:8318 ABCD2`. **Add a link** queues any `http(s)` video URL,
including YouTube links. **Save room to friends** keeps that invite, and saved
friends appear as one-tap **Join** rows the next time the panel opens; **X**
removes one. The list is stored in `partyfriends.json` next to the other app
data (at most 12 entries).

**All-time & friend history** opens the durable view. It ranks everyone this
device has watched with across every room, then lists each host with their
recent rooms, room codes, last title and accumulated time. Scores survive
leaving, closing the panel and restarting JukaHub; repeated state polls only
count new watch time. The archive is local and stored in
`partyhistory.json` next to the other app data. Selecting **Back to watch
party** returns; **B** / **Esc** does the same while the history is open.

Playback drift beyond a few seconds is corrected automatically; smaller
differences are left alone so the picture does not stutter. Only streams are
shared — local files stay on the device.

## Dock & ambient

Open from **Start → Ambient**; it also appears by itself after 45 seconds of
quiet while charging, or at the idle timeout on battery. The clock, weather,
now-playing card and photo slideshow are read-only — the chips along the bottom
control playback and the timers.

| Button | Action |
|--------|--------|
| A | Play / pause (starts the music folder when nothing is loaded) |
| Left / Right | Previous / next track |
| Up / Down | Volume |
| Y | Sleep timer: off → 15 → 30 → 60 → 90 min |
| X | Screen off, audio continues |
| N | Next photo |
| Any key / tap | Wake a darked panel |
| B / Start / Esc | Leave ambient mode |

The sleep timer stops playback before it blanks the panel; **X** only blanks the
panel. See [Dock & ambient](AMBIENT.md).

## Pomodoro

| Button | Action |
|--------|--------|
| A | Start / pause |
| X | Skip to the next phase |
| Y | Reset the session |
| B | Back |

## Alarm

| Button | Action |
|--------|--------|
| Up / Down | Move through alarms and fields |
| A | Select / save |
| X | Toggle the focused alarm |
| Y | New alarm |
| L1 | Delete the focused alarm |
| B | Back |

## Calculator

| Button | Action |
|--------|--------|
| D-pad | Move over the keypad |
| A | Press the focused key |
| L1 / R1 | Clear (**C**) / backspace (**<**) |
| B | Back |

## Terminal

The built-in shell, useful for diagnostics over the on-screen keyboard.

| Input | Action |
|-------|--------|
| Typed command | Runs it (saved to the history file, so it survives a restart) |
| Tab | Complete the command or path; on an empty line, open the on-screen keyboard |
| Up / Down | Command history |
| PgUp / PgDn | Scroll the output |
| Ctrl+L | Clear the output |
| Esc | Back |
| `help` | Built-in command list |

## JukaLand

| Button | Action |
|--------|--------|
| D-pad | Move |
| A | Jump |
| B | Break block |
| X | Place block |
| Y | Enter / exit the car |
| Start | Craft menu |
| Back / Select | Exit the game |

## Smart Pro Compatibility

Opened from **Emulators → Smart Pro Compatibility**.

| Button | Games tab | Core-builds tab |
|--------|-----------|-----------------|
| Up / Down | Move the selected game | Move the selected build |
| Left / Right | Switch tab | Switch tab |
| A | Apply the boost plan | Activate a pool build |
| X | Revert the selected plan | — |
| Y / R | Rescan ROMs, catalog and cores | Rescan |
| B | Back to Emulators | Back to Emulators |

## Desktop keyboard

| Key | Action |
|-----|--------|
| Arrows | Navigate |
| Enter | Confirm |
| Esc / B | Back |
| Q / E | Previous / next scene (not in the terminal, where they are ordinary characters) |
| Ctrl+K | Jump to the YouTube search screen |
| Ctrl+Q | Quick settings |
| Ctrl+Shift+V | Clipboard manager |
| Ctrl+Shift+S | Shorts feed (the Media **Y** action) |
| F1 | Shortcuts overlay |
| F2 | Global search |
| F8 | Watch Party panel |
| F11 / Alt+Enter | Toggle fullscreen |
| F12 | Screenshot |
| PgUp / PgDn | Page up / down (previous / next results page in Media) |
| Shift+Left / Shift+Right | Skip −10 / +10 s in the video player |
| Shift+Down (or N) / Shift+Up (or P) | Next / previous video in the player's list |

Mouse and touch work as well: clicking activates a card or tile, and hovering
highlights the card under the pointer.

---

## Related

- [Configuration reference](CONFIGURATION.md) — how a button in the config runs a trigger
- [Dock & ambient](AMBIENT.md) — the idle clock, slideshow, sleep timer and screen-off audio
- [Performance & compatibility](COMPATIBILITY.md) — per-game reports, boost plans and core builds
- [Building](BUILDING.md) — running a desktop build to use the keyboard bindings
