# Controls

JukaHub is controller-first: every screen is reachable with the D-pad and the
four face buttons. Keyboard and mouse are supported on desktop builds.

The top bar always shows the system status — a Wi-Fi meter, the weather chip,
the clock, a battery gauge, the now-playing title and the notification bell on
the right, the **Apps** pill and a **back** pill on the left. The bottom of every screen is the hint bar
for real controller bindings; there is no second clock or battery indicator
down there.

The **Wi-Fi meter** is the leftmost item of that right-hand cluster and a button
on every screen: four bars that are hollow when there is no link and fill with
signal strength, plus `! Offline` while the device has no network at all. A
click or tap opens the Wi-Fi picker (scan, connect, enter a password). From a
controller it is part of the same focus row as the clock and the forecast: on
Home **Up** from the Settings card reaches the header, then **Left**/**Right**
walk the forecast, the meter, the clock, the now-playing chip, the update offer
and the bell; in any other scene **Up** leaves the content for the top bar and
**Left**/**Right** walk it. **A** on the meter opens the picker, exactly like a
tap. The same picker is also
**Start → Wi-Fi Networks** and the Wi-Fi row in Quick Settings.

The **battery gauge** sits right of the clock: a cell filled to the charge left,
with the percentage beside it. It is green above 50%, yellow from 50% down to
20%, and red below that, so a dying battery is obvious from any screen.

The **media chip** closes that cluster. While something is playing it shows a
headphone glyph and the name of the audio track, or the video in the embedded
player. With nothing playing it names the output instead: the connected
Bluetooth device (only devices that actually accept audio, so a paired
controller is not mistaken for a headset) or **Headphones** when the 3.5mm jack
has a cable in. It is identical on Home and in every scene, and disappears only
when there is neither a session nor a connected output. A long name is
ellipsized (a quarter of the screen at most), and the chip is the first thing
the bar gives up when space runs out, before the network, clock or battery.

The chip is a button only while a player is behind it: a tap opens the audio
player for a track, and for a video it returns to the scene that owns the
player — expanding it first if it was left in its floating window. From a
controller the chip sits between the clock and the bell, so **Right** from the
clock lands on it on Home, and **Up** from the content reaches it in any scene;
**A** opens the same player. The idle output name is a reading, not a control:
it stays out of the focus order and a tap on it does nothing. The output is
re-probed in the background every few seconds, so plugging in headphones or
connecting a headset shows up without leaving the screen.

While media is loaded the chip shows its state without a second line of text:
the headphone glyph becomes two pause bars while playback is paused, so paused
never looks like playing. When the length of the track or video is known, a
hairline under the title fills in as it plays; a live stream or a file whose
length is still being probed simply gets no hairline rather than a made-up
position. An idle output name has no playback state, so it never shows the pause
bars or the hairline.

Every screen opens with the same bar, and the same Back control: the wordmark and
version, the screen's own name in the caption, then **Back** and **Menu**. **Back**
is the largest and heaviest control in that row — a step taller than the other
pills, in the accent colour — and it changes colour as soon as the D-pad reaches
it: tinted with accent type while idle, filled solid with dark type while it is
focused, with a ring around it. **A** (or a tap) on it leaves the screen, exactly
like **B**. The release offer, when a new version is advertised, is the pill
immediately left of the notification bell, in the same place on every screen.

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

### TrimUI Brick

The Brick (and the Hammer, the same board) has two small keys under the screen,
**F1** and **F2**. The firmware reports them as the stick clicks (L3 / R3), so
they used to do nothing in JukaHub. Now:

| Button | Action |
|--------|--------|
| F1 | The **Controller** card for the screen you are on: what every button does here. F1 or B closes it |
| F2 | **Quick Settings** (Wi-Fi, volume, brightness). F2 or B closes it |
| Menu | Back, like B and Select |
| Start | The Start menu: every scene, game and tool |
| Select + Start (hold both) | Quit JukaHub |
| L2 / R2 (the small top buttons) | Previous / next section on Home, bigger jumps in lists |

The audio player keeps L3 / R3 for lyrics and the sleep timer, the web browser
keeps L3 for switching between scrolling and walking links, and the game
library keeps R3 for a random game, so on those screens F1 / F2 do what those
screens say. On Home the footer names **Start** instead of a **B Back** that
had nothing to go back to.

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
| Start | Open the Start menu (scenes, games, tools, device) |
| Back / Select | Back one screen, falling back to Home |
| Select + Start (hold both) | Quit JukaHub and return to the launcher |

The TrimUI's **Select** button is the one the app sees as **Back**. Holding
**Select + Start** together quits the app the way every other launcher on the
card does: JukaHub saves, plays its goodbye screen, then exits. The pair is
read from the physical buttons before any panel sees it, so it also works from
a video, a settings panel or the on-screen keyboard — you never need the Home
screen or a mouse to leave the app.

The TrimUI Smart Pro's rumble motor echoes the beeps: a short tick when the
selection moves, a firmer bump on open, a longer jolt on an error. The handheld
motor is a vendor GPIO (`/sys/class/gpio/gpio227/value` with
`/sys/class/motor/voltage` on the TG5040 family, gpio236 and
`/sys/class/motor/level` on the Smart Pro S), not the pad's force-feedback node,
so JukaHub writes it directly; a USB or Bluetooth pad with rumble answers SDL
instead.

Feedback is on by default and can be switched off in **Settings → General →
Vibration**. That row only appears when a motor was actually found — a switch
that cannot do anything is not offered — and a motor that stops answering turns
the feedback off rather than retrying on every key press.

Shoulders are contextual, not scene-specific: whatever list holds the focus
owns them. **L1/R1** step one window and **L2/R2** jump further, so the pad
behaves the same in a menu as it does in a media grid.

| Screen | L1 / R1 | L2 / R2 |
|--------|---------|---------|
| Media results, Shorts grid, Apps grid | One screenful | Previous / next results page (R2 fetches it) |
| Favourites, packages, channel / file lists | One window of rows | First / last row |
| Text browser | Scroll a page | Zoom the page text out / in |
| Wallpaper picker | Five wallpapers | First / last wallpaper |
| Image viewer | Previous / next image | Jump ten images back / forward |
| Notification centre, clipboard history, global search, social notifications | One window of rows | First / last row |
| Quick Settings | Five rows, or ±5 on a slider | First / last row |
| Start menu | One window of the open group | First / last row of the open group |
| Home | — | Previous / next section |

## Start menu

**Start**, or the **Apps** pill in the top bar, opens one menu over whatever is
on screen: a rail of groups on the left and their entries on the right. The
footer names the scene the menu was opened over.

| Group | Holds |
|-------|-------|
| Scenes | Every scene in `jukaconfig.json` — the quickest way to switch screen |
| Games | Emulators, Games Library, Game Profiles, PS Remote Play, RetroAchievements, Save Manager, Windows Apps, PortMaster Ports, Smart Pro Compatibility |
| Tools | Downloads, Curation Hub, Audio settings, Smart Home, Remote Cast, Accessibility, Stick Calibration (when the install has one) |
| Device | Quick Settings, Wi-Fi Networks, Screenshot, Sleep Screen, Ambient, Wallpapers, About, Quit JukaHub |

Settings sub-screens and the maintenance scenes (Patch, Exit) are not listed:
**Settings** leads to them. Rows whose backend is missing on this device are
left out rather than shown greyed.

| Button | Action |
|--------|--------|
| Up / Down | Move through the open group; the rail switches groups when it has the focus |
| Left / Right | Move between the rail and the entries |
| A | Open the highlighted entry, or step into its group from the rail |
| B / Back / Start | Close the menu |
| L1 / R1 | One window of entries; L2 / R2 jump to the first / last row of the group |
| Mouse | Click a group, a row to open it, or outside the card to close |

**Start** itself opens this menu on every ordinary screen. The arcade (**Games**)
and **Ambient** bind Start to their own pause and exit, so the menu stays out of
their way, and no panel is covered by a menu drawn beneath it.

## Home

| Button | Action |
|--------|--------|
| A | Open the focused tile |
| B | Close the calendar popover, or the video that is still playing |
| D-pad | Move between tiles, the header chips (Wi-Fi meter, weather, clock, now-playing, update offer, bell, PS4/PS5) and the footer row |
| Left (from the weather chip) | Move onto the Wi-Fi meter; **A** opens the Wi-Fi picker |
| Right (from the clock) | Move onto the now-playing chip while something plays, then the update offer while a release is advertised, then the notification bell; **A** opens that player, installs the release, or opens the notification centre |
| Left (from the bell) | Move onto the update offer while a release is advertised; **A** installs it |
| L2 / R2 | Previous / next section of the grid |

## Media

| Button | Action |
|--------|--------|
| X | Search YouTube |
| Y | Shorts feed: opens Shorts, pre-loads it and plays the first clip, then keeps going after each one ends |
| **Shorts** button | The same feed, from the button row next to Search / Play Link / Trending |
| Left / Right | Select **Audio**, **Video** or **Chat** on the focused result row, then back to the row |
| A | Play the focused result, or press the highlighted **Audio** / **Video** / **Chat** button |
| L1 / R1 | Previous / next screenful of the loaded results |
| L2 / R2 | Previous / next results page; **R2** fetches the next page when it has not been loaded |
| B | Return from the row's buttons, then back out of Media |

Every result row carries three buttons at its right edge. **Audio** queues the
track in the Downloads panel (the same yt-dlp path, saved as an m4a under
`data/downloads/music`), **Video** queues the best picture and sound merged into
an MP4 under `data/downloads/video`, and **Chat** posts the link into the chat
as a message from you. The two downloads are separate because a track is small
enough to keep a whole library on the card and a video is not. With a row
focused, **Right** walks row → Audio → Video → Chat → row and **Left** walks the
same ring the other way, so **A** can save the sound, save the picture or share
a video without leaving the list. Tapping a pill works too, even on a row that
is not highlighted. The legacy multi-column grid keeps Left/Right for moving
between cards, so its buttons are tap-only.

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
| Up / Down | In scroll mode, scroll the page; at the top/bottom the focus goes to the header and the button row. In link mode, move to the nearest link on the row above/below |
| L1 / R1 | Scroll by a page |
| L2 / R2 (or the toolbar **zoom** chip) | Zoom the page text out / in (75%–160%); the chip shows the scale, lights up off 100%, and steps up on a tap |
| Left / Right | Walk the links in reading order, scrolling them into view |
| **Tab** (keyboard) or **L3** (left stick click) | Switch between scrolling the page and walking its links |
| A | Open the highlighted link, work the highlighted form control (type into a field, step a list, tick a box, send a form), or edit the address bar when nothing is picked |
| Y | Search the web (opens the keyboard); the **SEARCH** chip does the same |
| X | Open the full link list for the page |
| B | Leave the browser (in link mode, the first press returns to scroll mode) |
| `f` · **Find** chip | Find on the page; `n` / `p` step to the next / previous match, **Esc** clears the search |
| **Save** / **Marks** chips | Save the page on screen / open the saved-page list, whose chip carries its count |

Links are outlined as they come into focus, so a link deep in an article can be
followed without opening the picker. Walking them needs the D-pad to do two
jobs, so **Tab** on a keyboard, or a click on the left stick on a real gamepad,
cycles scroll mode and link mode; the toast names the keys. In link mode
Up/Down follow the page's rows and pick the nearest column, so a two-column
layout does not throw the cursor across the screen, while Left/Right stay in
plain reading order. **Y** (or the **SEARCH** chip beside the address bar) opens
the keyboard on an empty field: type a phrase to search, or a host to open it.
The **Back**, **Forward**, **Reload**, **Links**, **Filter**, **Search**,
**Home**, **Issues**, **Sites**, **Forget**, **Save**, **Marks** and **Find**
chips at the bottom are ordinary scene buttons, reachable by pressing Down past
the end of the page and Up to come back. **Sites** lists the hosts whose cookies
are on the card so one can be signed out on its own, and **Forget** drops them
all, so a page it is signed in to starts signed out; it asks twice, and the first
press says how many would go (see
[docs/BROWSER.md](BROWSER.md#forgetting-them)). **Save** keeps the page on
screen for later and **Marks** opens that list (see
[docs/BROWSER.md](BROWSER.md#saved-pages)).

The chip at the right end of the address bar reports how many ad and tracker
requests the page load skipped (`12 BLOCKED`, or `0 BLOCKED`). It turns amber as
**FILTER OFF** when the site is excepted, and tapping it is the same as the
**Filter** button: filtering is switched off for that host and the page reloads.
Press **Filter** again to put the site back under the filter.

## Recordings

Captures an emulator wrote into the shared folder, opened from
**Apps → Recordings**. See [docs/RECORDINGS.md](RECORDINGS.md) for how that
folder is chosen and what recording itself looks like.

| Button | Action |
|--------|--------|
| Up / Down | Walk the list, newest capture first |
| A | Play the highlighted capture |
| **Rescan** | Re-read the folder for a capture that just finished |
| **Media**, B | Leave the screen |

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
| B | Close the settings sheet, then step down: leave full screen, then float the video and go back |

The rewind and fast-forward buttons skip 10 seconds per press. With the settings
sheet open, **Up / Down** move between its rows and **A** applies the row, so
subtitles, full screen, speed and quality are reachable without a touch screen.
A tap on a control also moves the ring there, so touch and D-pad never disagree.

The **Quality** row cycles Auto → 360p → 480p → 720p → 1080p for the video on
screen only — Auto means the global *Playback resolution* setting. Picking a
quality re-resolves the stream and resumes playback at the current position, and
the pin is forgotten when another video starts. The re-resolve is a modal wait
like starting a video: the loading card names the source, the scene underneath
stops taking input, and **B** aborts it — the video that was already playing
keeps playing, because nothing is torn down until the new stream is ready.

On a keyboard **N** and **P** move to the next and previous video of the list
that launched the current one — in the Shorts feed that is the next short. In
the feed the four videos after the one playing are resolved in the background,
so skipping does not wait for yt-dlp.

**Back never stops the video.** It steps down one size: in full screen it
returns to the scene's player, and in the scene's player it shrinks the picture
into a small window in the bottom-right corner and opens the previous screen.
The session keeps playing — position, subtitles and audio are untouched — so
the Media grid, Home or any other screen is usable while the video runs. Tap
the window to return to the player; its **×** ends the session, as do the
player's Stop button and `q`. The dock and the screensaver cover the window
because they take the whole panel.

Progress, resume position and subtitles are handled automatically; the control
bar hides itself while playback continues and reappears on any input. When a
video reaches its end the player closes and returns to the scene — the frozen
last frame no longer keeps the grid behind it locked.

## Chat

The Chat screen shows the Discord channel (when configured) and the local
history. Every speaker can be opened as a profile card with their picture,
handle, roles and last message.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Scroll the history |
| D-pad Left / Right | Select the previous / next message |
| L1 / R1 | Walk the message selection one message at a time |
| L2 / R2 | Jump to the oldest / newest message |
| A | Type a message; with a message selected, open its actions |
| Y | Ask Juka AI about the typed text; with an empty composer, open the member list |
| X | Rename yourself in the chat |
| B / Back | Leave the chat, or close an open card |
| Tab (keyboard) | Open the member list |

**Names and pictures are the way to a profile**: click a speaker's name or
avatar in a bubble (or use **Actions → Profile** on a selected message). The
card offers **Mention**, **Latest** (jump to their newest message), **Copy ID**
and **Picture** (open the full image in the viewer); tapping the card's picture
does the same. The **Members N** chip in
the header opens the list of everyone in the history, so a name that has
scrolled away is still one press away.

A selected message turns the composer into its action bar: **Reply** (a quote
header in the bubble, sent as a real Discord reply), **Copy**, **Ask AI**,
**Profile**, and **Delete** for your own messages. The member list and the
profile card are modal: **Up/Down** walks the rows, **A** opens a profile,
**Left/Right** picks an action, and **B** closes.

## On-screen keyboard

Opens for search fields, the chat composer, the terminal and the unit converter.

| Button | Action |
|--------|--------|
| D-pad | Move between keys; **hold** to keep moving (320 ms, then every 70 ms) |
| A | Type the selected key |
| L1 | Shift (upper/lower case), without losing the focused key |
| R1 | Switch between the letters and the numbers/symbols layer |
| Y | Submit (send in chat, search elsewhere) |
| B / Back / Start | Close |

It also closes with its own close button, by tapping outside the panel, or when
you press **Done** / **Send**. A tapped key flashes as pressed, a moving mouse
pointer lights up the key it is over before you click, and an empty field keeps
its caret visible so it is obvious where typing will land.

## First-run tour

The first launch opens a short, skippable tour of the D-pad controls, top-bar
clock and notification bell, Appearance settings, and the Start menu. **A** or
**Enter** advances, **Left** goes back, and **B** or **Esc** skips.

Its **Get set up** page is a live checklist: Wi-Fi, RetroAchievements, the media
tools YouTube and IPTV use, Remote Play and the phone companion. Each row shows
whether it is already done and what is still missing; **Up**/**Down** chooses a
row, **A** opens the panel that fixes it, and the tour comes back when that
panel closes. Nothing on the page is required, and the states are read from the
device every time the page appears — there is no checklist progress to keep in
sync.

Replay it any time from **Settings → General → Replay tour**.

## Emulators

**Emulators** is a page of tiles, one per installed emulator, RetroArch core
and pinned tool. Each tile shows the emulator's own picture when the install
ships one — the `icon.png` (or `config.json` icon) inside a TrimUI `Emus/`
folder, or the icon Windows reports for an executable — and a gamepad glyph on
a tinted tile when it has none. Availability is a labelled pill at the bottom
of the tile — green **Available**, red **Unavailable** (installed without a
launcher), grey **Not installed** — and an unusable tile's picture is dimmed,
so the grid reads without opening anything. The footer names the highlighted
entry and its path.

| Input | Action |
|-------|--------|
| D-pad | Move between tiles; Up/Down move a whole row |
| **A** | Open the highlighted tool, launch the emulator, or install the emulator a system is missing |
| **X** | Install the highlighted system's emulator, or check its installed core and update it |
| **L1** | Update every installed emulator the buildbot has a newer build of |
| **R1** | Put back the build the last update replaced (only offered when one is kept) |
| **Y** | RetroAchievements |
| **B** / Esc | Back |
| Touch / mouse | Tap a tile to select and open it |

Pinned rows (Games Library, Game Profiles, Compatibility, BIOS Doctor,
RetroAchievements, Windows Apps, PortMaster) keep their place above the
emulators; they are doors rather than emulators, so a working pinned row shows
no pill. Emulator tiles below them are sorted by availability — usable first,
then installed-but-unusable, then absent — and alphabetically inside each
group. Rows with no emulator are no longer a dead end: **A** on one installs the
libretro core that runs that system, and **X** does the same for any tile while
also checking an installed core for a newer build. **L1** brings every installed
emulator forward in one pass. Only files in RetroArch's cores folder are
written: the download is checked against the CRC32 the buildbot index states,
the build is kept in the core pool with the URL it came from, and a binary that
is replaced stays beside the new one as
`<core>_libretro.so.jukahub-<date>.bak`.

That kept file is what **R1** restores. A nightly build that runs worse is one
press to undo: R1 installs the newest `.bak` over the active core, keeps the
build it just replaced as its own backup, and shows the rest of the work as a
toast. The footer names the date of the kept build next to the tile's status
(`GBA · Available · backup 2026-10-20`), so the key's presence is explained
before it is pressed. A second R1 press therefore goes forward again, and the
removed file is deleted only after the restore has landed, so a failed rollback
leaves the card exactly as it was. R1 is offered wherever a backup exists,
including Windows: the install policy is about what the buildbot serves, not
about restoring a file the card already holds.

No `Emus/<system>/` folder is ever written — those launcher scripts belong to
the firmware, and the standalone emulators upstream publishes are desktop
builds that would install a launcher that cannot start. On the Windows build the
install and update keys are not offered at all: the buildbot's aarch64 shared
object is not something a desktop RetroArch loads. R1 is the exception, because
it only ever restores a file the card already has.

## Game profiles

Open **Start → Game Profiles**, or the pinned **Game Profiles** tile in
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
**Start → Audio → Screen-off controls**: *Any key* wakes on the first press,
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
**Start → Audio**) turns the equaliser and the tempo filter off and hands the
decoded samples straight to the device; the two cannot be on at the same time.

**Shuffle**, **Repeat** and **Fav** also sit in the transport row and are tapped
directly on the touchscreen, which is the reliable path on a handheld whose pad
does not report a guide button.

On a keyboard, **Space** pauses, **L** toggles lyrics, **[** / **]** change the
listening speed, **T** cycles the sleep timer, **E** cycles the auto-exit timer,
**V** opens the full-screen visualizer, **M** toggles shuffle, **R** cycles
repeat off/all/one, **F** favorites the playing track, **Q** queues the
highlighted row to play next, **/** opens search, **G** opens the equaliser,
**O** turns the screen off, **K** opens **Start → Audio**, and **-** / **+** set
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

**Start → Audio** (or **K** in the player) opens the audio settings:

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

## Downloads

**Start → Tools → Downloads** keeps music on the card so the audio player works
without Wi-Fi. The card's own reading sits at the top — used, free, and how much
of it is JukaHub's saved music — and the queue below is grouped into
*Downloading*, *Queued*, *Failed* and *Saved*. The first row, **Add a link**,
opens the on-screen keyboard: paste a YouTube (or any `http(s)`) link and it is
queued as an m4a.

| Button | Action |
|--------|--------|
| Up / Down | Move through the list |
| Left / Right, L1 / R1 | Switch tab: All, Active, Saved |
| A | Add a link, play a saved track, or retry a failed one |
| X | Remove the highlighted row and delete its saved file |
| Y | Pause / resume the whole queue |
| B / Back / Start | Close |

One track downloads at a time: the handheld has 1 GB of shared RAM and one
yt-dlp is already a heavy child, so a second would starve the UI long before it
made anything faster. **Y** cancels the running transfer and puts its row back
in the queue, keeping the partial file, so resuming continues instead of
starting over. A track that fails keeps its error line and **A** retries it.

Saved tracks land in `data/downloads/music` and saved videos in
`data/downloads/video`. **A** on a saved track opens it in the audio player
with the rest of that folder as the playlist, so a downloaded album plays
straight through; **A** on a saved video opens it in the embedded video player.
Only `http(s)` links are accepted, and **X** only ever deletes files inside
those folders — the rest of the card is off limits.

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
landing page holds seven tools; **A** opens the selected one, **B** closes the
panel, and the list scrolls when the font is large. The footer names what the
selected row does, so the rows themselves stay plain.

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
names and read the Wi-Fi signal from `/proc/net/wireless`, falling back to
`wpa_cli signal_poll` when that table is missing or lists no interface (or
`netsh` on Windows). The report names the source it used, so it matches the
status meter.

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

**Memory scanner** opens the RAM scanner and **Controller test** opens the pad
diagnostic; both are described in their own sections below. Power tools closes
when either opens.

## Memory scanner

Open **Tools → Power tools → Memory scanner** to find and change a value in a
running game's memory. See [docs/MEMORY.md](MEMORY.md) for what the scan does and
what it cannot do.

| Input | Action |
|-------|--------|
| Up / Down | Move through the process list, the actions or the found addresses |
| PageUp / PageDown | Jump ten rows in the process list or the address table |
| Left / Right | Change the search width (1, 2, 4 or 8 bytes) on the actions page |
| A | Pick a process, open the value field, scan, narrow, browse results or write |
| Y | Narrow the scan to the value in the field (the same as the Narrow row) |
| R | Reload the process list on the picker page |
| B / Esc | Back from the address table, then close the panel |

The value field opens the on-screen keyboard on its digits layer. **ENTER** uses
the number; an empty field clears it. Decimal and `0x` hex are both accepted,
and a leading `-` is written as the two's complement the width allows.

Writes are two presses: the first arms the address and the second writes it.
There is no undo — the game keeps the new value until it overwrites it itself.

## Controller test

Open **Tools → Power tools → Controller test** to see the raw pad state. See
[docs/CONTROLLER_TEST.md](CONTROLLER_TEST.md) for the drift verdict.

| Input | Action |
|-------|--------|
| Click **Reset** / keyboard R | Clear every reading |
| Hold **SELECT** 1.2 s | Close the panel |
| Keyboard Esc, click outside | Close |

The panel consumes every pad event while it is open, so no button acts on the
UI — including B. Holding SELECT is the way out.

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
| Ctrl+L | Open the log viewer (the terminal keeps Ctrl+L to clear its output) |
| Ctrl+Q | Quick settings |
| Ctrl+Shift+V | Clipboard manager |
| Ctrl+C | Copy the text being edited: the open Notes field, the Terminal command line, the Chat draft, or the buffer of an open on-screen keyboard (also filed in the clipboard history) |
| Ctrl+V | Paste into whatever is being edited — the same Notes/Terminal/Chat and on-screen-keyboard fields — the way a keystroke would land (with nothing being edited, the key keeps its ordinary meaning) |
| Ctrl+Shift+S | Shorts feed (the Media **Y** action) |
| F1 | Shortcuts overlay |
| F2 | Global search |
| F8 | Watch Party panel |
| F11 / Alt+Enter | Toggle fullscreen |
| F12 | Screenshot |
| PgUp / PgDn | Page up / down (previous / next results page in Media) |
| Shift+Left / Shift+Right | Skip −10 / +10 s in the video player |
| N / P | Next / previous video in the player's list (also Shift+Down / Shift+Up) |

Mouse and touch work as well: clicking activates a card or tile, and hovering
highlights the card under the pointer.

---

## Related

- [Configuration reference](CONFIGURATION.md) — how a button in the config runs a trigger
- [Dock & ambient](AMBIENT.md) — the idle clock, slideshow, sleep timer and screen-off audio
- [Performance & compatibility](COMPATIBILITY.md) — per-game reports, boost plans and core builds
- [Building](BUILDING.md) — running a desktop build to use the keyboard bindings
