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

The bar is never focused when a screen opens: the ring starts on the screen's
own controls. On a screen with controls, **Up** from the top row steps onto the
bar and **Down** steps back into the content. On a screen with no controls of
its own (Playtime, the terminal, the audio player) there is nothing to step up
from, so **Up** goes straight to the bar and **Down** hands the pad back to the
page. Once a bar button holds the ring it answers to **A** before the screen
underneath: the back pill goes back, the Apps pill opens the menu, the clock and
the forecast open their cards, the Wi-Fi meter opens the picker and the bell
opens the notification centre, even where the same button does something else
in the scene below.

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

All bindings below are also listed in-app. On a keyboard, **F1** opens the
Controls card for the screen you are on, written in keyboard keys, and **F1**
again on that card opens the full shortcut list; **Esc** closes either. On a
controller the same card is the **?** row in Quick Settings (and the front
**F1** key on a Brick).

### Keyboard or controller: what the hints name

The hint bar, the Controls card, the on-screen keyboard's footer and the hint
lines inside panels name the controls you actually have
([player/inputhints.go](../player/inputhints.go)). On a PC without a game
controller every hint names keyboard keys: **Enter** where a pad would say A,
**Esc** for B, the arrow keys for the D-pad. Plug a controller in and the
hints switch to its buttons (a short message says so); press a key and they
switch back, press a button and they follow the pad again. On a TrimUI the
hints name the pad's buttons until a USB or Bluetooth keyboard is connected;
from then on they name its keys, and they go back to the buttons when the
D-pad is used or the keyboard is unplugged. The device's own power and volume
keys and a key mapper's virtual keyboard do not count as a keyboard.

**Settings → Controls → Button hints** overrides this: **Auto** follows the
rule above, **Controller** always names the pad's buttons, **Keyboard** always
names the keyboard's keys. The row's second line says what Auto is showing
and why.

| Pad | Keyboard |
|-----|----------|
| A | Enter (Space works too) |
| B, Select, Menu | Esc |
| D-pad | Arrow keys |
| X, Y | The X and Y keys |
| Start (the Start menu) | F10, which also closes it |
| L1 / R1 where they page a list | PgUp / PgDn |
| L2 / R2 where they jump to the first / last row | Home / End |
| L1 / R1 where they switch tabs | Tab (Shift+Tab goes back) |
| Select + Start (quit) | Alt+F4 on a PC; the pad's chord on a TrimUI |
| Quick Settings | Ctrl+Q, which also closes it |
| Search | F2 anywhere, **/** on Home |
| L2 / R2 on Home (sections) | Q / E |

A shoulder that does something else on a screen (seeks a video, clears the
calculator) keeps its pad name in the hint rather than promise a key that
does something different there.

Where a screen's key differs from these twins, its hint carries both, pad
button first (`X / C`, `L1/R1 / Tab`), and shows only the half you can press:
**C** on a keyboard, **X** on the pad. A held button reads the same way: Patch's
**Hold A** is **Hold Enter** on a keyboard.

Hints also follow **Accessibility** (Start → Tools → Accessibility): with the
Nintendo layout, an A/B or X/Y swap or a per-button remap, every hint names the
physical button that now does the job — **B Open** instead of **A Open** when A
and B are swapped — and the same goes for sentences that name a button. Keyboard
keys are never remapped, so keyboard hints do not change. Two things keep the
printed names on purpose: the **Select + Start** quit chord and the controller
test both read the physical buttons. An action no button sends any more (its
button switched off, or remapped onto another job with nothing in its place)
shows **—** instead of a button that now does something else or nothing.

### Keyboard keys, screen by screen

What each screen's footer buttons are on a keyboard, beyond the twins above
(checked against each screen's key handler, November 2026). A pad button that
no footer names may have no key.

| Screen | Pad | Keyboard |
|--------|-----|----------|
| Everywhere | A · B · D-pad · Start · Select + Start | Enter (or Space) · Esc · arrows · F10 · Alt+F4 on a PC |
| Home | L2 / R2 sections · Y search | Q / E · **/** (F2 anywhere) |
| Start menu | L1 / R1 · L2 / R2 · X power | PgUp / PgDn · Home / End · X |
| Notification centre | L1 / R1 · L2 / R2 | PgUp / PgDn · Home / End |
| Media | L1 / R1 tabs · R2 more · X search · Y details | Tab or [ / ] · PgDn · X or **/** · Y or I; U undo, Del remove |
| Favorites | Left / Right tabs · X remove · Y undo | Left / Right · X, D or Del · Y or U |
| Live TV | X search · Y favourite | X · Y or F |
| Browser | L1 / R1 scroll · L2 / R2 zoom · X links · Y search · L3 mode | PgUp / PgDn · - / + · X or L · Y or **/** · Tab; F find, N / P next / previous match, I site info |
| Video player | X captions · Y settings · L1 / R1 seek · L2 / R2 previous / next | X or C · Y or S · Shift+← / → · P / N (or Shift+↑ / ↓); F full screen, Q end playback, 1–4 speed |
| Chat | L1 / R1 tabs · Start send · X rename · Y tab action | Tab · Ctrl+Enter · X · Y; Enter types inline, 1–3 pick a tab |
| On-screen keyboard | A type · B close · X delete · Y submit · L1 / R1 shift / layer | type on the keyboard · Esc · Backspace · Enter · Shift; Ctrl+C / Ctrl+V |
| Settings | L1 / R1 sections · D-pad in an option sheet | Tab / Shift+Tab or PgUp / PgDn · arrows (Left / Right page the sheet) |
| Tools hub, Network tools | L1 / R1 section · X copy in a result sheet | PgUp / PgDn · X or C; R runs a tool, E edits its target |
| Wi-Fi | X details · Y scan · Select diagnostics · L1 / R1 page | X · Y or R · I · PgUp / PgDn; D disconnect, C reconnect, F forget |
| Files | Y actions · X mark · L1 / R1 page | Y · X · PgUp / PgDn; H hidden files |
| Image viewer | L1 / R1 zoom · L2 / R2 previous / next · Y fit · X lock | - / + · PgUp / PgDn · Y or F · X or L |
| PDF reader | L1 / R1 page (or chapter) · L2 / R2 zoom or text size · X text · Y options | PgUp / PgDn (or [ / ]) · - / + · X or T · Y, O or Tab |
| Playtime & achievements | L1 / R1 tabs · X sort · Y RetroAchievements | Tab / Shift+Tab · X · Y |
| Game details | Start options · X favourite · Y look up again | O · X or F · Y |
| Local play | L1 / R1 move a player · Y remove · X end game | PgUp / PgDn · Y or Del · X |
| Keyboard languages | L1 / R1 move · X remove · Y add | [ / ] · X or Del · Y |
| Config inspector | L1 / R1 view · X reset · Y export | Tab / Shift+Tab · X or Del · Y or E |
| Emulators | X install · Y alternative · L1 update · R1 roll back · R2 core builds | X · Y · U · R · K or C |
| Smart Pro Compatibility | Left / Right tab · X revert · Y rescan | Left / Right or Tab · X · Y or R; U updates |
| Downloads | Left / Right tabs · X remove · Y pause | Left / Right or Tab · X · Y |
| Save manager | X delete | X or Del |
| Curation Hub | X · Y | X or Del · Y; R refreshes |
| Game profiles, Windows apps | X · Y | X · Y or R |
| Power tools, Memory scanner | X · Y | X · Y (R picks the target in the scanner) |
| Notes | Y new · X delete · L1 / R1 folder | Y or N · X · F; S search |
| Calculator | X insert x · Y graph · L1 clear · R1 delete | X · Y or G · C · Backspace |
| Pomodoro | A start / pause · X skip · Y reset | Enter · X · Y |
| Alarm | X toggle · Y new · L1 delete (press twice) | X · Y or N · Del |
| Ambient | Y sleep timer · X screen off | Y or S · X; N next photo |
| Patch | Hold A to confirm a risky step | Hold Enter |
| Controller test | Hold Select closes | Esc closes, Enter or R resets |
| JukaLand | its own footer | A / D move, Space jump, F mine, E build, 1–0 block, P pause, Esc back |



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
card does: JukaHub saves, shows its teardown card while it stops what is
running, then exits. The pair is
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
| Notification centre, clipboard history, global search | One window of rows | First / last row |
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
| **Play Link** button | Ask for a link: opens the URL field on the keyboard with an empty buffer, and **Save** plays what was typed. A highlighted result plays first, and a link already in the field plays without asking again |
| Left / Right | Select **Audio**, **Video**, **Chat** or **Save** on the focused result row, then back to the row |
| A | Play the focused result, or press the highlighted **Audio** / **Video** / **Chat** / **Save** button |
| L1 / R1 | Previous / next screenful of the loaded results |
| L2 / R2 | Previous / next results page; **R2** fetches the next page when it has not been loaded |
| B | Return from the row's buttons, then back out of Media |

Every result row carries four buttons at its right edge. **Audio** queues the
track in the Downloads panel (the same yt-dlp path, saved as an m4a under
`data/downloads/music`), **Video** queues the best picture and sound merged into
an MP4 under `data/downloads/video`, **Chat** posts the link into the chat as a
message from you, and **Save** keeps the result in the Favorites library (the
pill reads **Saved** on a row that is already there, and pressing it again takes
the video out). The two downloads are separate because a track is small enough
to keep a whole library on the card and a video is not. With a row focused,
**Right** walks row → Audio → Video → Chat → Save → row and **Left** walks the
same ring the other way, so **A** can save the sound, save the picture, share a
video or bookmark it without leaving the list. Tapping a pill works too, even on
a row that is not highlighted. The legacy multi-column grid keeps Left/Right for
moving between cards, so its buttons are tap-only.

## Favorites

Favorites is the library, not a second copy of the search list. It opens on
**Continue** and has four tabs, each with its item count on the tab itself:

| Tab | What it holds |
|-----|---------------|
| **Continue** | Videos started but not finished, newest first, with the saved position |
| **Videos** | Everything saved with the **Save** pill on a result, grouped by channel |
| **Streams** | The channels kept on the IP Stream screen |
| **Recent** | Whatever was played last, grouped into Today / Yesterday / This week / Earlier |

| Button | Action |
|--------|--------|
| Left / Right | Previous / next tab |
| Up / Down | Move through the rows; past the last row reaches the Play / Remove buttons, and up from the first row reaches the header |
| L1 / R1 | Jump five rows; **L2 / R2** jump to the first / last row |
| A | Play the highlighted row — a video resumes where it left off, a stream opens the player, a file opens its folder |
| X | Remove the highlighted row from the tab it is shown on |
| B | Back out of Favorites |
| Tap a tab / row | Switch tab / play that row (the row map is recorded while drawing, so the tap resolves the row it landed on) |

The list scrolls only as far as it must to keep the focused row visible, a
heading is never scrolled over the row it belongs to, and every row shows a
16:9 frame (or a drawn tile for a stream), the title, who made it, and a resume
line when there is one. An empty tab says what it holds and how to fill it
instead of showing a blank panel.


The **Next page** chip appears in the bottom-right corner of the grid once the
focused card is on the last loaded row. Clicking it, pressing **R2**, or pressing
Down on the last card all load the next page of the same query and focus its
first result, so a search keeps going instead of stopping after the first batch.

## Live TV

### Theme gallery

The gallery of theme presets (Settings → Appearance) has two tabs: **Colour**
and **Total conversion**. A colour preset repaints the interface; a conversion
repaints it *and* rearranges the home screen, which is why they are not one
list.

| Button | Action |
|--------|--------|
| D-pad | Move between the cards of the open tab |
| A | Apply the highlighted preset |
| L1 / R1, or **Tab** / **Shift+Tab** | Switch tab (the grid resets to its first card) |
| Click a tab | Switch to it |

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
| L2 / R2 (keyboard **-** / **+**, or the toolbar **zoom** chip) | Zoom the page text out / in (75%–160%); the chip shows the scale, lights up off 100%, and steps up on a tap |
| Left / Right | Walk the links in reading order, scrolling them into view |
| **Tab** (keyboard) or **L3** (left stick click) | Switch between scrolling the page and walking its links |
| A | Open the highlighted link, work the highlighted form control (type into a field, step a list, tick a box, send a form), or edit the address bar when nothing is picked |
| Y (keyboard **Y** or **/**) | Search the web (opens the keyboard); the **SEARCH** chip does the same |
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
**Start → Scenes → Recordings**. See [docs/RECORDINGS.md](RECORDINGS.md) for
how that folder is chosen and what recording itself looks like.

| Button | Action |
|--------|--------|
| Up / Down | Walk the list, newest capture first |
| A | Play the highlighted capture |
| **Rescan** | Re-read the folder for a capture that just finished |
| **Media**, B | Leave the screen |

## Video player

The control bar is a focus ring: **Left / Right** walk it from the volume
(mute) button through rewind, play/pause and fast-forward — with previous and
next around them when the video came from a list — to speed, captions, settings
and full screen, and on to the two corner buttons; **A** presses the highlighted
button. With a mouse, the level bar beside the speaker sets the volume and the
seek row takes a click anywhere along the track.

The corners are two separate actions: **Close** (the X) ends playback and leaves
the video, and the downward chevron beside it steps the player down a size without
ending it — the same thing **B** does, and the way to reach the floating window
from a pad. On a touch screen the windowed player's controls take taps now, and
a tap anywhere else on the scene still belongs to the scene.

| Button | Action |
|--------|--------|
| Left / Right | Move the selection along the control bar |
| A | Press the selected control (play/pause when nothing is selected) |
| Up / Down | Volume |
| X (keyboard **X** or **C**) | Subtitles on / off |
| Y (keyboard **Y** or **S**) | Video settings sheet |
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
**Profile**, and **Delete** for your own messages. A message that carries a
picture leads with **Enlarge** — clicking the picture itself does the same
— which opens it full screen in the viewer, where it can be zoomed and
panned. The member list and the profile card are modal: **Up/Down** walks the rows, **A** opens a profile,
**Left/Right** picks an action, and **B** closes. The action bar belongs to the
panel that draws it: leaving the chat scene drops the selection, so a bar left
open can never take a press meant for the screen you are on.

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
| **Y** | The Xbox setup sheet on the Xbox tile (handheld) or xemu's tile (Windows); RetroAchievements on every other tile |
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

Apart from the Xbox tile below, no `Emus/<system>/` folder is ever written —
those launcher scripts belong to the firmware, and the standalone emulators
upstream publishes are desktop builds that would install a launcher that cannot
start. On Windows the same keys install and update a portable RetroArch, its
cores from the buildbot's Windows tree, and the standalone emulators from their
official releases.

### Xbox (hakuX on the Smart Pro)

The **Xbox** tile is always on the page. What it offers depends on the device:

| Device | Xbox tile |
|---|---|
| Smart Pro (TG5040) | Installs the experimental [hakuX port](https://github.com/ToastedFrog2/hakux-trimui-smart-pro) |
| Brick Pro | The same install, marked untested: same A133P chip, GPU and sticks, but the port is built for the Smart Pro |
| Brick, Hammer | Not supported: the port is built for the Smart Pro, and Xbox games need two analog sticks the Brick does not have |
| Smart Pro S | Not supported: the port is built for the Smart Pro's A133P and PowerVR GPU; no build exists for the A523 |
| Windows | xemu, installed from its official release; **Y** on its tile opens the BIOS checklist |

On the handheld tile, **X** installs or updates hakuX, **A** opens the Xbox
games in the Games Library (before the install, the setup sheet), **Y** opens
the setup sheet and **R1** rolls the last update back; **L1** includes hakuX
when JukaHub installed it. The install:

1. asks GitHub for the newest release (pre-releases included, since an
   experimental port may publish nothing else) and takes its
   `xbox-trimui-smart-pro-v<version>.zip`;
2. checks the download against GitHub's SHA-256 digest for that file, and
   against a checksum file attached to the release when there is one; the
   download's SHA-256 is recorded either way;
3. unpacks it into `Apps/JukaHub/data/hakux/stage/` on the same card and only
   then merges the package's `Data/` and `Emus/` folders into the card's root,
   file by file. Files the package does not ship (saves, shader caches,
   settings) are never touched; a file it replaces is kept in
   `Apps/JukaHub/data/hakux/backup/`, which is what **R1** restores; a merge
   that fails half way puts back everything it moved;
4. writes `Emus/XBOX/.jukahub-hakux.json`: the release, the asset URL, the
   digest, how the download was verified and every file laid down.

Games go in `Roms/XBOX` as `.iso` (XISO) images and start through the
package's own `Emus/XBOX/launch.sh`, like any other system. In a game,
**Menu** opens hakuX's settings (per-game settings, LAN, screenshots to
`Screenshots/XBOX`), and holding **Start + Select** for about 1.2 seconds
exits.

hakuX 1.0 and later run games on their own Xbox kernel and need no BIOS, MCPX
ROM or HDD image; the xemu-based builds before them did, and the port does not
say which core it was built from. So the setup sheet and the BIOS Doctor read
the installed build's own settings and list any firmware file they name, with
whether it is there; when they name none, the sheet says so.

Set expectations accordingly: this is an experimental port on a handheld with
1 GB of RAM. Not every game boots, 30 fps is a target rather than a promise, and
some games show graphics glitches. JukaHub installs the published package;
rebuilding it from the port's source is not automated.

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
the most-used screens, the 13-week heatmap and the badge list. Stats are stored
in the same user config as other settings and survive restarts.

There are **100 badges**, covering app time, days used, streaks, best days,
screens explored, one or more milestones per screen (games, music, video,
browser, files, settings, notes, terminal and the rest) and badges that count
the badges you already earned. The card lists what you have **already earned
first**, marked “Achieved first”, then the rest; each badge keeps its number
wherever it sits. An earned badge also shows **when** it was earned on the line
under its description (“Unlocked 2 Mar 2026”), while a locked badge leaves that
line empty. Every check reads the saved totals, so nothing unlocks from a
screen you have only visited.

The page is taller than the screen, so it scrolls. The page owns the D-pad and
the arrow keys on this screen — it has no buttons of its own, so the top bar is
reached with **Up** (which the page gives up at the top edge) rather than with a
step off a grid:

| Button | Action |
|--------|--------|
| D-pad Up / Down, arrow keys (or wheel) | Scroll the page |
| Page Up / Page Down, L1 / R1 | Scroll one screen |
| Home / End | Jump to the top / bottom |
| B | Back |
| Y | RetroAchievements |

A fresh visit starts at the top, and a scrollbar on the right shows where in the
page you are.

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
| Playlists | Left/right picks a saved list, **A** plays it, **X** saves the current queue under a typed name |
| Keyboard shortcuts | Opens the shortcuts reference |

Both layouts share the same hitboxes, so touch, keyboard and pad work the same
in either one. The choice and the opacity live in the config (`musicNowPlayingLayout`,
`musicArtOpacity`) and survive updates.

Saved playlists are named lists of the queue — "Road Trip", "Relaxing" — that
survive restarts and folder changes; they store paths, so a moved file is
skipped rather than breaking the list. Saving the same name again replaces that
list, and names are matched without regard to case.

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

## Files

**Start → Files** (also the **Files** tile and any `fileexplorer` shortcut) is a
file manager, not a directory dump. Above the list is a breadcrumb bar where
every folder in the path is its own chip — a deep folder is one press from the
card root instead of five **B** presses — with the folder's item count, its
size and the card's free space on the right and the sort chip beside them. Each
row carries a type chip (**DIR**, **IMG**, **AUD**, **VID**, **PDF**, **ZIP**,
**BOOK**, **TXT**, and **UP** for the parent), the name, then the size and the
day it was written. A photo or a video instead shows its own picture in that
chip slot — a video carries a small play mark on its first frame — so a folder
of pictures can be scanned by eye before anything is opened. Until the picture
is ready, or when there is none to make (no ffmpeg installed, an unreadable
format), the row keeps its type chip.

| Button | Action |
|--------|--------|
| Up / Down | Move through the list |
| L1 / R1 | Move a screenful; L2 / R2 jump to the first / last row |
| A | Open a folder or a file |
| B / Back | Up one level, or back to Home at the root |
| Y | The action sheet |
| X | Mark the row under the cursor and step down |
| Mouse / touch | A chip in the breadcrumb bar to jump, the sort chip to change order, a row to open it |

The action sheet (**Y**) holds **New folder**, **Rename**, **Copy**, **Cut**,
**Paste**, **Delete**, **Select all**, **Sort**, **Connect to device** and
**Remote files**. Marked rows are tinted and their type chip becomes a tick, so
a selection is visible in the list rather than only in the sheet. When nothing
is marked the actions apply to the row under the cursor, which is the rule a
desktop manager follows. **Delete** arms on the first press and acts on the
second, and a paste never overwrites: a second copy of the same name lands as
`name (2).ext`. The clipboard holds paths, so **Copy** and **Cut** work between
any two folders of either card without a PC.

**Connect to device** asks for an address on the on-screen keyboard —
`user@host:port/folder`, `host`, or `sftp://user@host/folder`, where a missing
user and port mean JukaHub's own SFTP account, so `192.168.1.7` is enough — and
opens **Remote files**, a browser for the other console's card. **A** downloads
the highlighted file into the folder that was open, **Y** uploads the marked (or
highlighted) local rows into the remote folder on screen, **X** takes another
address and **B** goes up a level. Transfers run in the background with their
progress in the panel's subtitle, one at a time; the first connection to a
device pins its host key and a later change is refused rather than trusted.

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
| L1 / `i` | Open the diagnostics sheet (it works even when no backend answered) |
| B / Esc | Close the picker, or leave the diagnostics sheet first |

A new secured network opens the on-screen keyboard for its password. The field
is masked, and **A** on the keyboard's key submits; closing the keyboard cancels.

In the diagnostics sheet, **A** re-reads the radio, **Y** / `c` copies the report
to the clipboard and writes a diagnostics bundle in the same press, and **B**
returns to the network list.

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
| Up / Down | Move through the rows, or scroll the changelog and BIOS Doctor |
| Enter / A | Open the changelog, report a bug, toggle diagnostics, or enlarge the **Diagnostics export** code |
| B / Esc | Leave a sub-page, then close the panel |

**Report a bug** writes a diagnostics file, copies the redacted report and
opens a GitHub issue with a summary, reproduction steps and diagnostics already
filled in. **Anonymous diagnostics** is off by default; when on, it sends only
the build, device model, platform, OS/architecture and panic counts — never
designs, filenames, links or account details.

## QR codes

Three codes exist to be scanned by another device: the **Phone Companion**
pairing code, the **Remote Cast** watch link, and **Community & support →
Diagnostics export**. In their cards they are a few hundred pixels square over
a dimmed screen, which is the shape a phone camera reads worst, so each one can
also be enlarged on its own.

| Input | Action |
|-------|--------|
| A (on the selected code), or a tap on it | Fill the screen with the code and raise the backlight to full |
| Up / Down / Left / Right (Remote Cast) | Select the code or the Start / Stop switch; the switch keeps the selection when the card opens |
| A / B / Esc, or any tap | Close the enlarged code and put the backlight back where it was |

On **Diagnostics export**, **B** still leaves the page and the enlarged code is
the page's only control, so **A** enlarges it instead of going back.

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

### The starfield idle screen

With **Ambient** off, the starfield is the idle screen. It keeps the clock, the
battery, a rotating quote and — if one was chosen in Files — a photo behind
them. The photo is set from the viewer, where **X** (or `l`) becomes **Set lock
screen**, and the same button there reads **Clear lock screen** once that
picture is the backdrop.

The idle screen can take the photo back itself, so clearing it does not need a
trip to Files. **Y** offers **Y clear photo** on its own line above the wake
hint, and a **Y** pressed again inside five seconds forgets the backdrop and
leaves the picture on the card. The first press only asks: every other input on
this screen means "wake up", and an ordinary wake must not throw the photo
away. While the first press is armed the line reads **Y again to clear** in
amber, and after the second it reads **Backdrop cleared · any key wakes** for a
few seconds. With no photo set there is no line at all, and **Y** wakes the
screen like any other key.

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
| L1 (keyboard **Del**) | Delete the focused alarm (press twice) |
| B | Back |

### When an alarm rings

A due alarm takes over the screen: a red wash blinks over everything — every
panel, a darkened display, even a game's own UI — and the sound loops until you
answer it. Nothing behind the card reacts while it rings.

| Button | Action |
|--------|--------|
| Left / Right (or Tab) | Choose Snooze or Dismiss |
| A / Enter / Space | Press the chosen button |
| S | Snooze 9 minutes |
| D / Esc | Dismiss |

Snooze silences it and rings again 9 minutes later, leaving tomorrow's schedule
alone; Dismiss ends it until the next scheduled ring. Tapping the two buttons
works too. The editor's **Ringtone** row cycles the six synthesized sounds with
Left / Right and previews each one as it lands.

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
