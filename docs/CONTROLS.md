# Controls

JukaHub is controller-first: every screen is reachable with the D-pad and the
four face buttons. Keyboard and mouse are supported on desktop builds. A
mouse pointer left alone for 10 seconds hides until the mouse moves or clicks
again (the wheel scrolls without bringing it back), on a PC and with a USB or
Bluetooth mouse on a handheld; the handheld's own pointer is the
[virtual cursor](#virtual-cursor).

Every action on a JukaHub screen sits on a button every supported handheld
has — the D-pad, **A**, **B**, **X**, **Y**, **L1**, **R1**, **L2**, **R2**,
**Select**, **Start** and **Menu** — or in that screen's menu, and the hints
name only buttons the handheld in your hands has. The stick clicks (L3 / R3)
are for games: the original Smart Pro's sticks do not press in, the Brick and
the Hammer have no sticks (their **F1** / **F2** keys do the same thing on
every screen), and only the Smart Pro S and the Brick Pro click theirs. Where
a screen has a menu of its own, **Select** opens it (the Browser, Mail, the
Games library); **Start** opens the Start menu, or the screen's options where
it has them (a game's options in the library, the music player's).

The top bar always shows the system status — a brightness button, a volume
button, a Bluetooth button, a Wi-Fi meter, the weather chip, the clock, a
battery gauge, the now-playing title and the notification bell on the right,
the **Apps** pill and a **back** pill on the left. The bottom of every screen is
the hint bar for real controller bindings; there is no second clock or battery
indicator down there.

The **Wi-Fi meter** sits immediately right of the Bluetooth button and is a button
on every screen: four bars that are hollow when there is no link and fill with
signal strength, plus `! Offline` while the device has no network at all. A
click or tap opens the Wi-Fi picker (scan, connect, enter a password). From a
controller it is part of the same focus row as the clock and the forecast: on
Home **Up** from the Settings card reaches the header, then **Left**/**Right**
walk the brightness, volume and Bluetooth buttons, the meter, the forecast, the clock, the now-playing
chip, the update offer and the bell; in any other scene **Up** leaves the content for the top bar and
**Left**/**Right** walk it. **A** on the meter opens the picker, exactly like a
tap. The same picker is also
**Start → Wi-Fi Networks** and the Wi-Fi row in Quick Settings.

The **Bluetooth button**, the Bluetooth rune, sits between the volume button and
the Wi-Fi meter wherever JukaHub can pair devices (a TrimUI, or a Linux PC with
`bluetoothctl`) and on Windows and macOS. The rune is struck through while the
radio is off and turns the accent colour, with a dot either side, while a device
is connected. **A** on it, or a click or tap, opens the
[Bluetooth page](#bluetooth-devices) with the ring on the first paired device,
so a second **A** connects or disconnects it; on Windows and macOS the page
offers the system's own Bluetooth settings. A crowded screen bar gives the
button up after the forecast.

The **volume button**, a speaker, sits immediately left of the Bluetooth
button, wherever JukaHub can drive a mixer: the codec on a TrimUI,
JukaHub's own entry in the Windows volume mixer, PulseAudio or ALSA on a Linux
PC. Its glyph follows the level, with a cross while muted. **A** on it, or a
click or tap, opens the **volume card**: the level, a slider and a mute button.
In the card **Left**/**Down** turn the level down and **Right**/**Up** turn it
up in 5% steps (hold to repeat), **A** mutes and un-mutes, and **B** or
**Start** closes it. On a keyboard the arrow keys, **-**/**+** or
**PgDn**/**PgUp** change the level, **Enter**, **Space** or **M** mutes, and
**Esc** or **F10** closes. With a mouse, click or drag the slider, click the
card's speaker to mute, scroll on the card or over the button, and click
outside to close. The level follows the hardware volume keys, and Quick
Settings' volume row shows the same value.

The **brightness button**, a sun, sits immediately left of the volume button
and leads the cluster; the sun is smaller below half brightness. **A** on it,
or a click or tap, opens the **brightness card**: the level, a slider and a
**Dim** button. In the card **Left**/**Down** dim the screen and
**Right**/**Up** brighten it (hold to repeat), **A** dims to the lowest level
and brightens back to where it was, and **B** or **Start** closes it. On a
keyboard the arrow keys, **-**/**+** or **PgDn**/**PgUp** change the level,
**Enter**, **Space** or **D** dims, and **Esc** or **F10** closes. With a mouse,
click or drag the slider, click the card's sun to dim, scroll on the card or
over the button, and click outside to close. The level never goes below a floor
that keeps the screen readable.

What the slider moves depends on the device. On a TrimUI Smart Pro or Brick it
is the panel's backlight, through the same display driver the firmware uses,
in the firmware's own ten steps; the level is written back to the firmware's
settings, so the stock Settings slider and JukaHub stay in sync. The stock
Smart Pro has no dedicated brightness shortcut. On a Linux PC it is the backlight; on a Windows laptop or handheld the
panel brightness (the level Windows' own slider shows), and on a Windows desktop
the monitor's brightness over DDC/CI. Where there is no such control the card
dims JukaHub's picture instead and says so in its last line. Quick Settings'
brightness row and **Settings → Display → Brightness** show the same
value, and the full-screen pairing QR still borrows full brightness while it is
up and gives the level back when it closes.

The **battery gauge** sits right of the clock: a cell filled to the charge left,
with the percentage beside it. It is green above 50%, yellow from 50% down to
20%, and red below that, so a dying battery is obvious from any screen.
While a charger is connected the gauge turns green with a bolt over the cell;
on the Smart Pro and the Brick the charger input decides, because the
battery's own status is not reliable there.

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
like **B**. Immediately left of the notification bell sits a gold trophy:
**A** (or a tap) on it opens **Trophies** from any screen, a gold count on it
says how many trophies were earned since the trophy room was last open, and it
is outlined while you are in the room. The release offer, when a new version
is advertised, is the pill left of the trophy, in the same place on every
screen.

The bar is never focused when a screen opens: the ring starts on the screen's
own controls. On a screen with controls, **Up** from the top row steps onto the
bar and **Down** steps back into the content. On a screen with no controls of
its own (Playtime, the terminal, the audio player) there is nothing to step up
from, so **Up** goes straight to the bar and **Down** hands the pad back to the
page. Once a bar button holds the ring it answers to **A** before the screen
underneath: the back pill goes back, the Apps pill opens the menu, the clock and
the forecast open their cards, the Bluetooth button opens the Bluetooth page,
the Wi-Fi meter opens the picker, the trophy
opens Trophies and the bell opens the notification centre, even where the same button does something else
in the scene below.

The header chips are buttons on every screen: the weather chip
(`21C / 12C`, or `69F / 53F` in the US) opens **Local weather**, a seven-day
forecast for the detected location; the time opens the calendar. The chip uses
Fahrenheit automatically in the US and its territories, and Celsius elsewhere,
until a unit is set explicitly in Settings. Both chips can be tapped or reached
with the D-pad — Up from the top-right cards lands on the weather chip, then
the clock, and **Right** from the clock lands on the notification bell. **A** on
the bell opens the notification centre, exactly like tapping it; **B**/Esc
closes it. The highlighted notification shows its whole message, and the
D-pad, **L1**/**R1**, **L2**/**R2** and the mouse wheel scroll through every
one kept; a toast cut short with "…" opens the centre on its message when
clicked. **A** runs the highlighted row's action ("Install update", "Open",
"Retry", "Play" — the pill at the row's end; a row without one closes the
centre), **X** dismisses the row and **Y** twice clears them all (Enter,
Delete / X, Y and Shift+Delete on a keyboard; a tap on the pill or the "×").
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
| B, Select (and Menu with **Menu button: voice** off) | Esc |
| D-pad | Arrow keys |
| X, Y | The X and Y keys |
| Start (the Start menu) | F10, which also closes it |
| L1 / R1 where they page a list | PgUp / PgDn |
| L2 / R2 where they jump to the first / last row | Home / End |
| L1 / R1 where they switch tabs | Tab (Shift+Tab goes back) |
| Select + Start (quit) | Alt+F4 on a PC; the pad's chord on a TrimUI |
| Quick Settings | Ctrl+Q, which also closes it |
| Search | F2 anywhere, **/** on Home |
| Menu: the voice orb (hold it to talk) | F4 anywhere (hold it to talk) |
| Menu held: screenshot (in a game too) | F12 |
| L2 on Home (Edit Home) · R2 (liquid background level) | Q · W (Home only) |
| L2 in Quick Settings (Edit Quick Settings) | Q (or Home) |
| Select + L1 held (mouse mode) | F6 |

A shoulder that does something else on a screen (seeks a video, clears the
calculator) keeps its pad name in the hint rather than promise a key that
does something different there.

Where a screen's key differs from these twins, its hint carries both, pad
button first (`X / C`, `L1/R1 / Tab`), and shows only the half you can press:
**C** on a keyboard, **X** on the pad. A held button reads the same way: Patch's
**Hold A** is **Hold Enter** on a keyboard.

**Face buttons on a TrimUI.** The Smart Pro and the Brick print a Nintendo
layout on their face buttons (A right, B bottom, X top, Y left), while their
firmware reports button positions the Xbox way. JukaHub turns both pairs back
where every press enters the app, so the button printed X does what every X
hint says, and the same for A, B and Y; the phone remote, control profiles and
JukaLand follow the same rule, and the Controller test lights the letter you
pressed. **Settings → Games & emulators** has the two switches, **Swap A and
B** and **Swap X and Y**: both are on by default on the handheld and off on
Windows; turn one off for a pad that already reports that pair the way it is
printed. If you remapped X and Y in Accessibility to work around the old
behaviour, set them back.

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
| Everywhere | A · B · D-pad · Start · Menu (the voice orb) · Select + Start · Select + L1 held (mouse mode) | Enter (or Space) · Esc · arrows · F10 · F4 · Alt+F4 on a PC · F6 |
| Home | L2 edit Home · R2 liquid background · Y search | Q · W · **/** (F2 anywhere) |
| Home search sheet | X edit the search · Y voice command | type to edit |
| Home (Carousel, Widgets) | X edit (L2 too) | X (Q too) |
| Home edit mode | X resize · Y remove · L1 / R1 layout · B or L2 done | X or R · Del or Backspace · Shift+Tab / Tab · Esc or Q |
| Home edit mode, resizing | D-pad size · A or B done | arrows · Enter or Esc |
| Start menu | L1 / R1 · L2 / R2 · X power | PgUp / PgDn · Home / End · X |
| Notification centre | L1 / R1 · L2 / R2 | PgUp / PgDn · Home / End |
| Media | L1 / R1 tabs · R2 more · X search · Y details | Tab or [ / ] · PgDn · X or **/** · Y or I; U undo, Del remove |
| Favorites | Left / Right tabs · X remove · Y undo | Left / Right · X, D or Del · Y or U |
| Live TV | X search · Y favourite | X · Y or F |
| Browser | Select menu (link mode, site info, …) · L1 / R1 scroll · L2 / R2 zoom · X links · Y search | M or the Menu key · PgUp / PgDn · - / + · X or L · Y or **/** · Tab; F find, N / P next / previous match, I site info, Backspace or Alt+← / Alt+→ back / forward, F5 reload |
| Video player | X captions · Y settings · L1 / R1 seek · L2 / R2 previous / next | X or C · Y or S · Shift+← / → or J / L · P / N (or Shift+↑ / ↓); K play / pause, M mute, F full screen, Q end playback, 1–4 speed |
| Chat | L1 / R1 tabs (Voice after Saved) · Start send · X attach files · Y tab action | Tab · Ctrl+Enter · X · Y; Enter types inline, 1–3 pick a tab, 4 Voice, R renames you, Del drops a waiting file |
| On-screen keyboard | A type · Start send / submit · B close · X delete · Y submit (Ask AI in chat) · L1 / R1 shift / layer · R2 voice typing | type on the keyboard · Esc · Backspace · Enter · Shift; Ctrl+C / Ctrl+V · F4 voice typing |
| Settings | L1 / R1 sections · Y search settings · A on "More settings (N)" unfolds the rest · D-pad in an option sheet | Tab / Shift+Tab or PgUp / PgDn · Ctrl+F or type a letter on the rail to search · arrows (Left / Right page the sheet) |
| Tools hub, Network tools | L1 / R1 section · Y search the hub · X pin a tool on the hub, copy in a result sheet | PgUp / PgDn · Y, / or Ctrl+F search · X pin (X or C copies in a sheet); R runs a tool, E edits its target |
| Voice command | A or Menu speak / stop · X type · Y phone QR · B back (cancels while listening or during the pause) | Enter · X · Y · Esc; F4 speak / stop |
| Wi-Fi | X details · Y scan · Select diagnostics · L1 / R1 page | X · Y or R · I · PgUp / PgDn; D disconnect, C reconnect, F forget |
| Files | Y actions · X mark · L1 / R1 page (in two panes: left / right pane) | Y · X · PgUp / PgDn; H hidden files; Tab other pane, F5 / F6 copy / move to it |
| Image viewer | L1 / R1 zoom · L2 / R2 previous / next · Y fit · X options (rotate, rename, delete, lock screen) · Start mark up in Paint | - / + · PgUp / PgDn · Y or F · X or Tab options · L lock · R rotate · F2 rename · Del delete · M |
| PDF reader | L1 / R1 page (or chapter) · L2 / R2 zoom or text size · X text · Y options | PgUp / PgDn (or [ / ]) · - / + · X or T · Y, O or Tab |
| Playtime & achievements | L1 / R1 tabs · X sort · Y RetroAchievements | Tab / Shift+Tab · X · Y |
| Game details | Start options · X favourite · Y look up again | O · X or F · Y |
| Games library | Select menu (random game, tag filter, search, box art, rescan) · R1 search · L1 fill missing box art · Y rescan · Start a game's options | M · S or **/** · F · Y · O; R random game, T tag filter |
| Games library, a console's page | L2 / R2 previous / next letter (hold to keep going) · Right next letter in a column | PgUp / PgDn · → |
| Local play | L1 / R1 move a player · Y remove · X end game | PgUp / PgDn · Y or Del · X |
| Keyboard languages | L1 / R1 move · X remove · Y add | [ / ] · X or Del · Y |
| Config inspector | L1 / R1 view · X reset · Y export | Tab / Shift+Tab · X or Del · Y or E |
| Emulators | X install · Y alternative · L1 update · R1 roll back · R2 core builds | X · Y · U · R · K or C |
| Smart Pro Compatibility | Left / Right tab · X revert · Y rescan | Left / Right or Tab · X · Y or R; U updates |
| Downloads | Left / Right tabs · X remove · Y pause | Left / Right or Tab · X · Y |
| Save manager | X delete | X or Del |
| Curation Hub | X · Y | X or Del · Y; R refreshes |
| Game profiles | X · Y | X · Y or R |
| Windows apps | X setup · Y display · Start rescan | X · Y or D · R or F5 |
| APK Store | A options · X details · Y rescan · L1 / R1 tabs | Enter · X or Del · Y · Tab / Shift+Tab or PgUp / PgDn |
| Power tools, Memory scanner | X · Y | X · Y (R picks the target in the scanner) |
| Notes | Y new · X delete · L1 / R1 folder · Select menu | Y or N · X · F · M; S search, Ctrl+S every note as a file |
| Calculator | X insert x (history: edit) · Y graph · L1 clear · R1 delete | type the sum · Y · Del · Backspace |
| Pomodoro | A start / pause · X skip · Y reset | Enter · X · Y |
| Tasks | A done · Y add · X task / list sheet · L1 / R1 list · Left / Right column. Due picker: L1 / R1 ±7 days / ±1 hour, Y save | Enter or Space · N or Y · X · PgUp / PgDn · ← / → or Tab; Del twice deletes a task |
| Voice Memos | Y record · A play / pause (stop & save while recording) · X options (pause while recording) · L1 / R1 −5 s / +5 s | Y or R · Enter · X (Space pauses a recording) · ← / →; N rename, Del delete |
| Paint | A draw · X tools · Y colours · L1 / R1 undo / redo · L2 / R2 zoom · Select file menu · left stick glide | Enter or Space · T · C · Z or Ctrl+Z / Ctrl+Y · - / + (0 fit) · M; [ ] brush, 1–8 tools, G grid, Ctrl+S save, Shift+arrows 8 px |
| Stopwatch | A start / pause · X lap · Y reset (countdown being set: next preset) · L1 / R1 tab · countdown Left / Right field, Up / Down change | Enter · X · Y · Tab · arrows |
| Solitaire | A pick up / put down · X draw · Y smart move · L1 / R1 undo / redo · Start menu | Enter or Space · D · F or Tab · Z / R (Ctrl+Z / Ctrl+Y) · M; N new game |
| Synth | Start play / stop · L1 / R1 page · L2 / R2 pattern or instrument · Pattern: A step, Y / X note (accent / ratchet on drums) | Space · Tab / Shift+Tab · [ ] · Enter, Y or =, X or -; 1–7 play the pads |
| Pads | Start play / stop · L1 / R1 page · Play: A pad, Y sound, X edit, Select finger drums, L2 / R2 bank · Beat: A step (hold to paint), Y / X note, Select accent · sticks play effects | Space · Tab / Shift+Tab · [ ] · Enter · M or ` (Select); 1–4, Q–R, A–F, Z–V play the pads |
| Alarm | List: X toggle · Y new · L1 delete (press twice). Editor: Y save · X music / clear label · L1 / R1 ±10 min | X · Y or N · Del; editor Y or S · PgUp / PgDn |
| Ambient | Y sleep timer · X screen off | Y or S · X; N next photo |
| Patch | Hold A to confirm a risky step | Hold Enter |
| Controller test | Hold Select closes | Esc closes, Enter or R resets |
| JukaLand | its own footer | A / D move, Space jump, F mine, E build, 1–0 block, P pause, Esc back |



### TrimUI Brick

The Brick (and the Hammer, the same board) has two small keys under the screen,
**F1** and **F2**. The firmware reports them as the stick clicks (L3 / R3), so
they used to do nothing in JukaHub. Now, by default:

| Button | Action |
|--------|--------|
| F1 | The **Controller** card for the screen you are on: what every button does here. F1 or B closes it |
| F2 | **Quick Settings** (Wi-Fi, volume, brightness). F2 or B closes it |
| F1 (hold) / F2 in a RetroArch game | F1 held plays the game backwards, F2 switches fast forward on and off ([Game hotkeys](#game-hotkeys-retroarch-games); Settings → Controls → **Game hotkeys**) |
| Menu | The voice orb: press it and speak (with **Menu button: voice** off in Settings → Controls, Back like B) |
| Menu (hold 0.7 s, let go) | Screenshot of the screen, in a game too (Settings → Controls → **Hold Menu: screenshot**; off, holding Menu is push-to-talk). In a game it can open the game switcher instead, or do nothing (**Hold Menu in a game**) |
| Start | The Start menu: every scene, game and tool |
| Select + Start (hold both) | Quit JukaHub |
| Select + L1 (hold) | Mouse mode: the D-pad moves a pointer and A clicks (see Virtual cursor) |
| L2 / R2 (the small top buttons) | On Home L2 edits Home; R2 steps the liquid background (Off, Slight, Medium, Lively) on any screen that has no R2 job of its own; L2 jumps to the top of a list |

F1 and F2 do this on every screen: no JukaHub screen gives the stick clicks a
job of its own (lyrics and the sleep timer are in the music player's
**Start** options, link mode and site info in the browser's **Select** menu,
the random game and the tag filter in the Games library's **Select** menu). The
Games library takes every press while it is open, the front keys too, so they
do nothing there. On Home the footer names **Start** instead of a **B Back**
that had nothing to go back to.

**Settings → Controls → F1 key** and **F2 key** (Brick and Hammer only) pick
what each key does instead: *Controller card*, *Quick Settings*, *Screenshot*,
*Game switcher*, *Search*, *Start menu*, *Sleep*, *Mute* (or back to the last
level), *Home*, or *Nothing* (the key does nothing in JukaHub; a game still
gets it as L3 / R3). The key that opens the Controller card, Quick Settings,
the Start menu or Search also closes it, and the Controller card lists what
both keys do.

### TrimUI Smart Pro S: the Home key

The Smart Pro S has a **Home** key on its top edge, beside the power key. A
tap on it (let go within about a second; a longer hold is left alone) does what
two rows in **Settings → Controls** say:

| Row | Choices |
|-----|---------|
| Home key | *Home* (the default: closes what is open and shows Home), *Quick Settings*, *Game switcher*, *Nothing* |
| Home key in a game | *In-game menu* (the default, as if Select + L2 were held), *RetroArch menu* (RetroArch's own menu in RetroArch games; other emulators get the in-game menu), *Game switcher* (the game saves and quits to the switcher), *Nothing* |

The rows appear once JukaHub has found the key. Its code is not published:
JukaHub reads an input device that sends `KEY_HOMEPAGE` (172) or `KEY_HOME`
(102). If the rows never appear, run `evtest` over SSH, press Home, and put the
code it shows as `"homeKeyCode": "<code>"` in `jukauser.json`'s `variables`
([Configuration](CONFIGURATION.md)); JukaHub's log names the device it reads
(`Home key: reading key …`). The firmware still sees the key: on the stock
menu, its own quick menu may open as well. On the idle screen or a dark dock a
tap only wakes the screen, and over a panel with work of its own (Ports, the
save manager, a download list…) it does nothing until B closes it.

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
| L2 / R2 | Bigger jump: previous / next page (fetching the next page of Media results), previous / next video in the player, first / last row in a list; on Home L2 edits Home; on every screen whose footer names no R2 job (it shows **R2 Water** instead) R2 steps the liquid background Off → Slight → Medium → Lively, in place of the old jump to the last row |
| Start | Open the Start menu (scenes, games, tools, device) |
| Back / Select | Back one screen, falling back to Home |
| Select + Start (hold both) | Quit JukaHub and return to the launcher |
| Right stick | Move the virtual cursor (below); L2 / R2 scroll under it |
| Select + L1 (hold) | Mouse mode on / off: the pointer stays on and the D-pad and sticks move it (below) |
| Select + R1 in a game (TrimUI) | Screenshot of the game's screen ([below](#screenshot-in-a-game-select--r1)) |
| Power (short press, handheld) | Sleep — on Home, any screen or mid-game; the game carries on at wake (Settings → Power & battery → Power button: Sleep, Exit card or Nothing) |
| Power (hold 1.5 s) on a JukaHub screen | The power card below — or, with Settings → Power & battery → **Ask before power off** off, power off at once |
| FN switch (Smart Pro, with Settings → Power & battery → Travel lock on) | Locks the power button: no wake, and a power-on turns straight back off; slide it back to unlock |

### Virtual cursor

The **right stick** is a mouse pointer (handy on the TrimUI Smart Pro, which
has no touch screen): move it and the pointer appears and follows it — a light
tilt is slow, full tilt crosses the screen in about a second and a half. **A**
clicks (hold A and move to drag a slider or a shelf), and **L2 / R2** scroll up
and down under the pointer (held, they keep scrolling). B and the other buttons
keep their usual jobs; the **left stick** stays the D-pad's twin. Any D-pad
press or left-stick step puts the pointer away (so A presses the focused item
again), and so do 10 seconds without touching the right stick; the right stick
brings it back where it was. A stick that rests a little off centre never
brings it up.

Screens that use the right stick themselves keep it: JukaLand and browser
games aim with it, and the stick calibration and the controller test measure
it. Chat shares it: up / down scrolls the history, and a sideways tilt brings
the pointer up so the paperclip, links and chips can be clicked with **A**;
once the D-pad puts the pointer away, up / down scrolls again. The pointer also stays away in Quick Settings
and the wallpaper picker. Turn it off in **Settings → Controls → Virtual
cursor**.

#### Mouse mode (hold Select + L1)

Hold **Select + L1** for about a second (0.7 s) to turn **mouse mode** on, and
again to turn it off; **F6** does the same on a keyboard. A short message says
which. In mouse mode the pointer stays on, and the
**D-pad** moves it as well as the sticks: slowly at first, for a precise nudge,
then faster the longer you hold it, up to the sticks' full speed. The **left
stick** moves it too instead of the focus. **A** clicks (hold A and move to
drag), **L2 / R2** scroll under the pointer, and **B** and every other button
keep their usual jobs, so you are never stuck behind the pointer: B still goes
back. This is how the **TrimUI Brick**, which has no sticks, gets a pointer.

Left alone for 10 seconds (no move, click or scroll), the pointer hides where
it is; mouse mode stays on. The next move of the D-pad or a stick, or **L2 /
R2**, brings it back at the same spot and moves or scrolls as usual. The first
**A** only brings it back, so nothing is clicked that you cannot see: press
**A** again to click. A panel that closes while the pointer is hidden leaves
it hidden.

Screens that own the pad put the pointer away until they close: Quick
Settings, the wallpaper picker, the stick calibrations, the controller test, a
ringing alarm, JukaLand and browser games; it also goes while the screensaver
is up. The on-screen keyboard keeps the D-pad for its keys (mouse mode resumes
when it closes). In Chat the right
stick still scrolls the history while the D-pad and the left stick move the
pointer. **Settings → Controls → Mouse mode** turns the chord (and F6) off.

GammaOS, an Android firmware for the same handhelds, turns its mouse mode on
with Select + R1. In JukaHub, holding Select and pressing R1 already steps
through the control profiles (**Settings → Controls → Switch hotkey**), so
mouse mode uses the mirror image, **Select + L1**, which nothing else uses. The
chord is read from the physical buttons, like the quit chord, so it is the same
in every control profile and with any Accessibility remap; a control-profile
line for `select+l1` only runs with Mouse mode turned off. Press Select first:
an L1 pressed before Select still turns the page it always does.

#### Mouse in games and apps

**Settings → Controls → Mouse in games and apps** (TrimUI handhelds; off until
you turn it on) carries the same chord into games, emulators and ports. While
one is running, hold **Select + L1** and the pad becomes a mouse for it: JukaHub
first lets go of every button the game saw pressed and puts the sticks back at
rest, then takes the pad for itself (the game stops seeing it) and adds a
virtual mouse called "JukaHub Mouse". The sticks and the D-pad move it at the
same speeds as in JukaHub, **A** is the left button (hold it to drag), **B**
the right button, **X** the middle button, and **L2 / R2** the wheel. The
vibration motor gives a firm pulse when the mouse comes on, a short one when it
goes, and a long one if it could not. Hold **Select + L1** again, or press
**Menu**, to give the pad back; leaving the game or quitting JukaHub gives it
back too.

**Only programs that draw their own pointer show one**: ScummVM, DOSBox, PC
ports through PortMaster. Most emulated games have no use for a
mouse and ignore it, and a program that only looks for input devices when it
starts may not notice a mouse that appears later. The game sees Select and L1
go down before the mouse takes over, so in a game that uses them they do their
job there first — and if your RetroArch uses **Select** as its hotkey button,
Select + L1 may be one of its shortcuts (often *Load State*): change that
shortcut or leave this switch off. While the pad is the mouse, neither the
game nor the firmware's own pad shortcuts see its buttons, and an alarm that
rings gives the pad back to JukaHub.

A **Windows program** (Windows Apps, a Wine game profile) gets the mouse
without the switch: the pad is its mouse from the start, the D-pad types arrow
keys and a few buttons type Enter, Esc, Tab and Space, and Select + Start
closes the window (held, it ends the program). The table is under
[Windows apps](#windows-apps).

#### Screenshot in a game (Select + R1)

On a TrimUI handheld, press **Select + R1** together in any game, emulator or
port JukaHub started and the screen is saved as a PNG: the picture as it was
the moment both buttons were down, before the game has drawn anything for the
press. There is no hold. Each press is one picture (keep Select down and press
R1 again for the next), at most one a second. The motor gives a firm pulse
once the file is written, and the "While you were playing" note back in
JukaHub counts the shots. They go where **Hold Menu** game shots go
(`game_<time>.png` in JukaHub's `screenshots` folder; see
`menuHoldScreenshot` in [Configuration](CONFIGURATION.md)).

The game sees Select and R1 too, so a game or emulator that has a job for the
pair does it as well: turn the chord off with **Settings → Controls → Select +
R1: screenshot** if that gets in the way. Pressed with Menu held it is left to
the emulator's hotkeys, and while the games mouse has the pad it is not seen.
In JukaHub's own menus, holding Select and pressing R1 still steps through the
control profiles.

#### End a stuck game (hold Select + Start)

On a TrimUI handheld, hold **Select + Start** for three seconds in any game,
emulator or port JukaHub started and the game ends: it is asked to quit (so
it can still save) and, if it has not after six seconds, stopped. Nothing is
drawn and the game is not paused first, so this works when the screen is
black, when the game ignores the buttons, or when the in-game menu cannot
come up over it. The motor gives a firm pulse when the hold counts. A tap or
a short hold of Select + Start is still the game's. A PSP game JukaHub was
watching for its picture (the A133P models, [GAME_LIBRARY.md](GAME_LIBRARY.md#launching))
says more than the hold: when its picture never reached the screen, PSP
games get the screen to themselves from the next start (JukaHub closes while
one plays; a toast says so), and when it did, nothing changes. Where JukaHub
could not look, a hold within a minute of JukaHub's own PPSSPP starting
still switches PSP games to the firmware's PPSSPP for the next game;
choosing JukaHub's PPSSPP on the PSP tile switches back.

In a PSP game, **Menu** also opens PPSSPP's own pause menu, whose **Exit**
comes back to JukaHub.

#### In-game menu (hold Select + L2)

With **Settings → Controls → In-game menu** on (TrimUI handhelds; on by
default), hold **Select + L2** for half a second in any game, emulator
(RetroArch, PPSSPP, …) or port JukaHub started. The game pauses, its picture
stays on screen, dimmed, and a small menu opens over it:

| Row | What it does |
|-----|--------------|
| Resume game | The game carries on where it was |
| Save state / Load state | For a RetroArch game started from the library: choose one of six slots, shown three at a time with screenshots and dates |
| Rewind | On lighter RetroArch systems, step back through recent gameplay; the launch enables RetroArch's rewind only for that game |
| Fast forward / Normal speed | Toggle RetroArch's faster play, then return to normal speed from the same row |
| Disc | For a multi-disc RetroArch game started from the library: "Disc 1 of 3"; Left/Right pick another disc, A puts it in and the game carries on ([Games library](GAME_LIBRARY.md#which-disc-a-game-starts-on)) |
| Cheats | For a RetroArch game started from the library with cheats switched on in its options: the whole cheat list with On/Off switches (see below) |
| Ask Juka | The game assistant listens (only with Game assistant on); B comes back to the menu |
| Sleep | Put the handheld to sleep and resume the game on wake |
| Quit to JukaHub | The emulator is asked to quit (the same request as quitting JukaHub mid-game: RetroArch and PPSSPP write SRAM and settings on the way out) and JukaHub's Home comes back; one that has not quit after six seconds is ended |
| Game switcher | Quits the game as Quit to JukaHub does (RetroArch writes its auto save), then opens the [Game switcher](#game-switcher) (only with Settings → Games & emulators → Game switcher on) |
| Stopwatch / Timer | Once the [Stopwatch](#everyday-apps) or its countdown has been started: "Stopwatch 12:34.5" or "Timer 4:59 left", live; **A** starts or pauses it, **Right** takes a lap |
| Quick settings | Brightness, volume, mute, Wi-Fi, Bluetooth, the stick lights, rumble and Power off without leaving the game, with CPU, temperature, memory and battery at the top (see below) |

| Button | Action |
|--------|--------|
| D-pad up / down | Move the highlight |
| A | Choose |
| B, Menu, or Select + L2 again (a press, no hold) | Back to the game |
| Select + R2 | Ask Juka straight away |

The save, load, rewind and fast-forward rows send commands to RetroArch; the
other rows work over any game. Like the assistant, the game sees Select and L2 go down before JukaHub
takes the pad (an emulator hotkey on Select + L2 still fires), the game
resumes by itself after three minutes, and a ringing alarm takes over. Save
states are the emulator's own (RetroArch's menu on Menu); quitting keeps what
the game itself saved. Holding Select + L2 over the assistant switches to the
menu. **Up/Down** moves through all six save slots, changing page after slot
three. **A** saves or loads the highlighted slot; saving over an occupied slot
asks for a second **A**.

**Cheats** lists every cheat of the file chosen in the game's options
(**Start → Cheats** in the library), in RetroArch's order. **A** switches the
highlighted cheat on or off, **Left/Right** jump a page. **B** with nothing
changed goes back to the menu; **B** after a change gives the game back and has
RetroArch switch those cheats at once (it shows its own "Cheat #… [ON]"
message for each). The choice is also kept for the game's next start, as if it
had been made in the options. The row only shows when JukaHub wrote the
game's cheat file for this start: a cheat file you saved yourself in RetroArch,
or a NextUI pak game, keeps RetroArch's (or NextUI's) own cheat menu. If
RetroArch's cheat menu or cheat hotkeys were used in the same game, or a
command could not be sent, the list says that changes apply from the next
start.

**Quick settings** opens a card over the paused game. At the top: CPU load
and clock, temperature, memory used and the battery with its time left, read
once a second (the game is paused, so the CPU figure is the handheld at rest).
**Up/Down** pick a row, **Left/Right** change it, **A** switches it.
**Brightness** and **Volume** change at once (A dims or mutes); **Mute** turns
the speaker off and back to its last level; **Music in game** (only while
JukaHub's music plays under the game) is how loud it plays, heard at once.
**Wi-Fi** and **Bluetooth** take a few seconds; the row says "Switching…" and
the card keeps working. **Lights** is Settings → Stick lights → Lights in
games (with JukaHub driving the stick lights). On a handheld with a motor,
**Rumble** is RetroArch's rumble strength, used from the next game you start
(RetroArch cannot change it while it runs), and **Vibration** is JukaHub's own
buzz. **Power off** asks for a second **A** (unless Ask before power off is
off), gives the game back to save — RetroArch writes a save state, another
emulator is closed cleanly — and switches the handheld off. **B** goes back
to the menu, **Select + L2** straight to the game.

**Settings → Controls → In-game menu entries** chooses which rows the menu
shows: A on a row shows or hides it, and the list stays open for the next.
Resume game is always first and Quit to JukaHub always last. A hidden row
still appears while it is the only way to stop something (Stop recording,
Normal speed).

#### Game hotkeys (RetroArch games)

In a RetroArch game started from the library (TrimUI handhelds, with
**In-game menu** on), a few presses work without opening the menu. Nothing is
taken from the game: it still sees every press. The menu's bottom line lists
the paused game's hotkeys.

| Button | Action |
|--------|--------|
| F1 held (Brick, Hammer) or L3 held (Smart Pro S, Brick Pro) | The game plays backwards while it is held; let go and it carries on from there. Only on the systems whose in-game menu has **Rewind** |
| F2 or R3 | Fast forward on; press again for normal speed (the in-game menu's row follows) |
| Menu + Select (press together, let go) | Save and quit to JukaHub: "Saved — it continues where you left off", and the next start of the game picks up there. A deliberate quit like this does not start the game by itself at the next power-on |

**Settings → Controls → Game hotkeys** is *Auto* (F1 / F2 on a Brick or
Hammer, L3 / R3 on a handheld whose sticks press in), the pair itself, or
*Off*. The original Smart Pro's sticks do not click, so it has no pair and no
row: rewind and fast forward are the in-game menu's rows and RetroArch's own
hotkeys there.
**Menu + Select: save and quit** turns that press off. A press made while
Select, Start or Menu is held belongs to a chord and does nothing here.
Nintendo DS games keep the keys to themselves (the DS cores tap the screen
and close the lid with L3 / R3), and on a handheld whose sticks click so do
PlayStation and DOS games, and light-gun games (the gun re-aims on its spare
R shoulder).
Menu + Select does nothing in RetroAchievements hardcore (RetroArch saves no
states there), in PPSSPP, ports and NextUI paks. With auto-resume off
(`savestateResume: false`) it writes a save state in the current slot
instead, and the in-game menu's **Load state** brings it back.

#### Game buttons and turbo (RetroArch games)

**Settings → Controls → Game buttons for** picks *All consoles* or one
console; the **Buttons** and **Turbo** rows under it then change that
console's games (a console starts at *Default*, which follows All consoles).
A single game can choose its own in the library: **Start** on the game →
**Buttons** / **Turbo** (*Default · …* follows its console).

| Choice | What it does |
|--------|--------------|
| Buttons: Normal | Every button does what its letter says |
| Buttons: Swap A and B | A does what B does in the game, and B what A does |
| Buttons: Swap X and Y | The same for X and Y |
| Buttons: Swap both | A/B and X/Y swapped: the Xbox layout |
| Turbo: A or B | Holding that button fires it over and over |
| Turbo speed | Fast, Normal or Slow (about 15, 10 or 6 presses a second), for every console |

They apply to RetroArch games started from the library, for that start only:
JukaHub writes a remap file for the game and sets RetroArch's Turbo Fire, and
both are removed when RetroArch closes, so games started from the firmware's
own menu keep RetroArch's settings. RetroArch's own menu and hotkeys are not
swapped. A remap file you saved yourself in RetroArch (for the game, its
folder or its core) is never replaced: that game keeps your file, and the
Buttons row says so. Turbo works out of the box on RetroArch 1.21 and newer;
on older RetroArch it needs the pad's A and B buttons in RetroArch's controls
(the Turbo row says so when they are missing), and another button pressed
while the turbo button is held fires repeatedly too. Standalone emulators and
NextUI paks keep their own controls.

**Button layout** (Settings → Controls, the first row; handhelds) sets the
whole pad at once: **Nintendo** (the default: the right button, A, confirms, as
printed) or **Xbox** (the bottom button confirms). Xbox swaps A/B and X/Y in
JukaHub's menus (the same switch as Accessibility's button layout; the hints
then name the button to press) and sets *All consoles* **Buttons** to **Swap
both**; Nintendo turns both back, the Buttons row returning to what it was
before Xbox. A console or game with its own Buttons keeps them, and picking a
layout clears Accessibility's per-button remaps. Under Xbox, **Button hints
show** picks **Printed letters** (the letter on the button to press: the bottom
one is B) or **Roles** (its job in the Xbox layout: A confirms). PPSSPP,
PortMaster ports and NextUI paks keep the printed layout.

**D-pad as stick**, under Turbo, makes the D-pad push the left analog stick,
for games that only move with the stick: on a Brick (no sticks) N64, PSP and
Dreamcast games. *All consoles* is **Auto** (on for the N64, PSP and Dreamcast
on a handheld without sticks, off everywhere else) or **Off**; a console picked
on **Game buttons for** gets *Default*, **On** or **Off**. The stick is either
at rest or pushed all the way, so there is no slow walk, and the game no longer
sees those D-pad presses as the D-pad. It goes into the same one-launch remap
file as Buttons (a remap of your own still wins, and a control layout's
buttons win over it).

**Left stick as D-pad**, on a handheld with sticks (the Smart Pro, Smart Pro
S and Brick Pro), is the other way round: the left stick works the D-pad, for
the games of consoles that had no stick. *All consoles* is **Auto** (the
default), **On**, **Off** or **RetroArch's own**; a console picked on **Game
buttons for** gets *Default · …* (what All consoles gives it), **On**, **Off**
or **RetroArch's own**. Auto is on for every console whose pad had no stick
(the Saturn, the DS and the arcade systems included: their pads are digital,
so the stick would otherwise do nothing) and off for the N64, PSP,
Dreamcast, PlayStation 2, GameCube and Wii, whose games read the stick. A
PlayStation game follows its pad: on the DualShock (**Analog controller**)
both sticks stay sticks whatever this row says; on the standard pad the stick
is its D-pad. It sets RetroArch's *Analog to digital type* (left stick, not
forced) for that start only; a remap of your own that sets it still wins. The
row is hidden on the Brick and the Hammer, which have no sticks.

#### Control layouts (RetroArch games)

A control layout is a whole way of holding the pad for one kind of game. Open
a game's page → **More** → **Controls**, or **Start** on the game in the
library → **Controls layout**. The list shows the layouts for that console and
the focused one's chart; **A** uses it for this game, **X** for every game of
the console, **B** goes back. *None (console buttons)* plays with the
console's Buttons and Turbo choices. The game page shows a *Controls: …* chip
while a layout is in use.

| Layout | Consoles | What it does |
|--------|----------|--------------|
| Arcade 6-button | Arcade | Light/medium/heavy punch on Y, X, L1; kicks on B, A, R1 (FinalBurn Neo: L2/R2 all three punches/kicks) |
| Arcade 4-button (Neo Geo) | Neo Geo, Neo Geo CD, Arcade | Neo Geo A, B, C, D on Y, B, X, A |
| DS stylus | Nintendo DS | Right stick moves the stylus (the D-pad on a Brick, which then is not the DS D-pad), R2 taps, L2 swaps the screens (melonDS DS: next screen layout; DeSmuME: L2 closes the lid) |
| ScummVM point and click | ScummVM | D-pad or stick moves the pointer, A left click, B right click, X Escape, Y skip a line, L1 slow pointer, R1 F5, Start the ScummVM menu, Select the keyboard |
| DOS: the game's own mapping | DOS | DOSBox Pure's own mapping for the games it knows; Select opens its on-screen keyboard |
| DOS keyboard / DOS mouse | DOS | Keyboard- or mouse-style DOS games; Select opens the on-screen keyboard (docs/GAME_LIBRARY.md, "ScummVM and DOS shelves") |
| Light gun | NES, SNES, Mega Drive, Master System, Sega CD, PlayStation, Arcade | Right stick aims (the D-pad on a Brick), R1 fires, L1 reloads, R2 re-centres (R1 when R2 fires); tuned and tried per game in **More** → **Light gun** ([LIGHT_GUN.md](LIGHT_GUN.md)) |

A layout decides only the buttons it sets; the Buttons choice (Swap A and B
…) still decides the rest. Like Buttons it is written for that start only and
removed when RetroArch closes, and a remap file you saved yourself in
RetroArch wins. For DS cores it also sets the core's touch option for that
start (melonDS: touch mode Joystick; DeSmuME: right-stick pointer) — start the
game once from RetroArch first so the core has an options file. Standalone
emulators and NextUI paks keep their own controls, and the picker says so. The
arcade charts assume the core's default device type ("Classic"); if you
changed it in RetroArch, use None.

**Your own layouts.** Put a `.json` file in JukaHub's data folder under
`control-layouts/` (read at start and whenever the list opens; up to 64 files,
32 KB each):

```json
{
  "schemaVersion": 1,
  "id": "kof-mine",
  "name": "KOF my way",
  "description": "Punches on top",
  "systems": ["Neo Geo", "Arcade"],
  "defaultFor": [],
  "buttons": { "y": "b", "x": "y", "b": "a", "a": "x" },
  "coreOptions": { "fbneo-cpu-speed-adjust": "100%" },
  "variants": [
    { "cores": ["mame2003_plus"], "noSticks": false, "buttons": { "l": "none" } }
  ],
  "chart": [ { "button": "Y", "action": "Light punch" } ]
}
```

- `buttons`: pad button → what the game gets. Names: `a b x y l r l2 r2 l3 r3 start select up down left right`, `none`, or a full stick push `lstick-up/-down/-left/-right`, `rstick-…`.
- `coreOptions`: libretro core options for that start (no quotes or line breaks in values).
- `variants`: laid over the layout for the listed cores (`fbneo`, `mame2003_plus`, `melonds`, `melondsds`, `desmume` …) and/or only on a device without sticks (`noSticks`).
- `keys` (pad button → keyboard key such as `enter`, `escape`, `space`, `f1`, `a`) is accepted and shown but not sent yet.
- `id`: lower-case letters, digits and dashes (the file name when left out); it cannot reuse a built-in layout's id. `defaultFor` makes it the layout for those consoles' games unless a game picks another.

Choices are kept in `data/game-layouts.json`.

#### Light gun (RetroArch gun games)

Gun games aim with the right stick (the D-pad on a Brick), fire with R1 and
reload with L1; R2 (R1 when R2 is the trigger) puts the crosshair back in the middle. Known gun games
(Duck Hunt, Time Crisis, Operation Wolf …) start with it once by themselves.
A game's page → **More** → **Light gun** turns it on or off and sets the
aim (Move or Point), speed, aim area and buttons, with a practice target. On
Windows the mouse is the gun. Details: [LIGHT_GUN.md](LIGHT_GUN.md).

#### Ask Juka in a game (hold Select + R2)

With **Settings → Voice & AI → Game assistant** on (TrimUI handhelds; on by
default), hold **Select + R2** for about a second (0.7 s) in a game, an
emulator or a port JukaHub started. The game pauses, its picture stays on
screen, dimmed, and the voice orb listens: ask about the game. Juka's answer
appears in a wide bubble and is read aloud (**Read answers aloud in games**,
on by default, even with Juka's voice off).

| Button | Action |
|--------|--------|
| A or R2 | While Juka listens, send now; on an answer, ask again |
| B, Menu, or Select + R2 again (a press, no hold) | Back to the game (it carries on where it was) |

The game sees Select and R2 go down before JukaHub takes the pad, then nothing
until it carries on; it also resumes by itself after three minutes, and a
ringing alarm takes over. See
[VOICE.md](VOICE.md#asking-about-the-game-youre-playing).

### Stick calibration

**Settings → Controls → Analog stick calibration** returns to the TrimUI menu.
Open **Setting → System → Calibrate Joystick** there and calibrate both built-in
sticks. The firmware applies this calibration before games and emulators read
the sticks. This option appears on TrimUI devices with built-in sticks.

**Settings → Controls → JukaHub stick calibration** measures both sticks in about ten
seconds: let go of them, roll them slowly round their edges, let go again.
JukaHub then knows where each stick really rests (a drifting stick no longer
moves the focus or the pointer on its own), how much it jitters there (its
dead zone), and how far it reaches (so a stick that falls short still reaches
full speed). The result is drawn with the live sticks moving through it; A
saves it, B cancels, and **Reset to default** forgets it. A step that sees a
thumb on a stick starts over; a doubtful result (a short roll, a stick resting
far off centre) says why under that stick and puts the focus on **Measure
again** instead of Save. Each stick's dead zone can also be set by hand
(**Left / Right stick dead zone**, 5–50 %); the next calibration replaces it.

The JukaHub option tunes stick navigation, the virtual cursor, and what counts
as stick movement for the screensaver. Games and emulators read the sticks
themselves, so use the device-wide option for their hardware range. PPSSPP's
own calibration (Start menu → Tools → Stick Calibration) remains available for
PPSSPP-specific dead zones. The Controller test (Power tools) draws
each stick's dead zone ring around its calibrated resting spot.

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
| Wallpaper picker | Five rows | First / last row |
| Image viewer | Previous / next image | Jump ten images back / forward |
| Notification centre, clipboard history, global search | One window of rows | First / last row |
| Quick Settings | ±5 on a focused slider; on Tiles and Compact the previous / next value of a focused mode row; on the List, five rows. While editing: the layout | L2: Edit Quick Settings (L2 again is Done) · R2: the last button |
| Start menu | One window of the open group | First / last row of the open group |
| Home | — | L2: Edit Home (L2 again is Done) · R2: liquid background Off → Slight → Medium → Lively |

### Liquid background

With **Settings → Theme & look → Liquid background** on (*Gentle* or
*Lively*), the backdrop is a shallow pool and the controls you already use move
it. It only watches: nothing is taken from the screen in front of it.

**Start → Device → Liquid background** switches it off or back on in one press;
the menu shows **On** or **Off**, and switching it back on restores *Gentle* or
*Lively* as last chosen. A new setup starts with *Gentle* on.

| Input | The water |
|-------|-----------|
| Moving the focus (D-pad, left stick, arrow keys) | A ripple under the tile it lands on |
| Left stick, while tilted (Smart Pro) | Stirs the water round the focused tile |
| Right stick (Smart Pro) | Drags a wake on its own, or follows the virtual cursor while it is up |
| Pointer (Brick, mouse mode) or mouse (PC) | Drags a wake |
| Tilting the device | Sloshes it — only where an accelerometer answered (**Tilt to slosh**) |

Reduce Motion keeps the ripples you make and drops the rain and the slosh.

### Wallpaper picker

**Settings → Theme & look → Wallpaper** or **Start → Device → Wallpapers**
lists pictures and GIFs (a GIF plays as the wallpaper), folders first, with a
preview of the highlighted file. It opens in the current wallpaper's folder.

| Input | Action |
|-------|--------|
| Up / Down | Move |
| A / Enter | Set the picture, open a folder, go up a folder, or on *None* go back to the glass backdrop |
| Right | Open the highlighted folder |
| Left / Backspace | Up a folder; the folder you came from stays selected |
| L1 / R1, PgUp / PgDn | Five rows |
| L2 / R2, Home / End | First / last row |
| B / Esc | Close |
| Mouse (PC) | A click highlights a row, a second click (or a double-click) uses it; the wheel moves three rows |

## Start menu

**Start**, or the **Apps** pill in the top bar, opens one menu over whatever is
on screen: a rail of groups on the left and their entries on the right. The
footer names the scene the menu was opened over.

| Group | Holds |
|-------|-------|
| Screens | Home, then Trophies (with how many you have earned, e.g. "12/386"), then every other screen in `jukaconfig.json` — the quickest way to switch screen |
| Games | Emulators, Games Library, Game Profiles, PS Remote Play, Xbox Remote Play, Steam, RetroAchievements, Save Manager, Windows Apps, PortMaster Ports, APK Store, Smart Pro Compatibility |
| Tools | Downloads, Curation Hub, Audio settings, Smart Home, Screen Share, Voice command, Accessibility, Stick Calibration (when the install has one) |
| Device | Quick Settings, Liquid background, Wi-Fi Networks, Screenshot, Sleep Screen, Ambient, Wallpapers, Edit Home, Phone Companion, About, Quit JukaHub |

Under the groups, the rail shows the **Phone Companion** pairing code whenever
the handheld has a network address and the menu has room for a readable code:
scan it straight from the menu, or tap it to fill the screen with it (PIN and
address underneath). **Device → Phone Companion** opens the pairing card.
Screen Share paints over the code, as it does on Home.

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
| Mouse | Click a group, a row to open it, the pairing code to enlarge it, or outside the card to close |

**Start** itself opens this menu on every ordinary screen. The arcade (**Games**)
and **Ambient** bind Start to their own pause and exit, so the menu stays out of
their way, and no panel is covered by a menu drawn beneath it.

## Home

| Button | Action |
|--------|--------|
| A | Open the focused tile |
| B | Close the calendar popover, or the video that is still playing |
| Select | Open [Quick Settings](#quick-settings); press Select or B to close it |
| X | The [Game switcher](#game-switcher) (Standard and Hero layouts; on the Carousel and Widgets X is Edit Home, on the Tabs Home the item sheet) |
| D-pad | Move between tiles, the header chips (brightness button, volume button, Bluetooth button, Wi-Fi meter, weather, clock, now-playing, update offer, bell, PS4/PS5, pairing QR) and the footer row |
| Left (from the weather chip) | Move onto the Wi-Fi meter; **A** opens the Wi-Fi picker |
| Left (from the Wi-Fi meter) | Move onto the Bluetooth button; **A** opens the Bluetooth page |
| Left (from the Bluetooth button) | Move onto the volume button; **A** opens the volume card |
| Left (from the volume button) | Move onto the brightness button; **A** opens the brightness card |
| Right (from the clock) | Move onto the now-playing chip while something plays, then the update offer while a release is advertised, then the notification bell; **A** opens that player, installs the release, or opens the notification centre |
| Left (from the bell) | Move onto the update offer while a release is advertised; **A** installs it |
| Right (from the PS5 pill), Down (from the bell) | Move onto the pairing QR while it is shown; **A** (or a click or tap) shows it full screen at full brightness with the PIN and address under it, and **A** or **B** closes it |
| L2 | Edit Home, in any layout; L2 again is Done ([Edit Home](#edit-home)) |
| R2 | The liquid background: Off → Slight → Medium → Lively (a toast names the level; the footer shows **R2 Water**, **W** on a keyboard). R2 does the same on every other screen whose footer shows **R2 Water** |

### Game switcher

Recent games side by side, each with a screenshot of where you left it (its save state's picture, else its box art). Open it with X on Home, Start → Games → Game switcher, search, or the in-game menu.

**Settings → Controls → Hold Menu in a game → Game switcher** makes holding
Menu for 0.7 s in a game (then letting go) quit it the way the in-game menu's
row does and open the switcher. **Settings → Power & battery → Start in →
Game switcher** opens it when the handheld is switched on.

| Button | Action |
|--------|--------|
| Left / Right | Previous / next game |
| L1 / R1 | First / last game |
| A | Carry on from the auto save |
| X | Take it off the recent games (its saves stay) |
| B | Back |

### Quick menu

A full-screen menu: big Recents, Collections, Games and Tools tiles over
round Wi-Fi, Sleep, Restart, Power off and Settings buttons. Open it from
Start → Device → Quick menu, or set **Settings → Controls → Menu button on
Home** to *Quick menu* so a tap of Menu on Home opens it (and closes it again).
D-pad moves, A opens, B closes. Theme pictures and every way in:
[Quick menu](QUICK_MENU.md).

### Home layouts

**Settings → Theme & look → Home layout** picks one of five layouts:
**Tabs** (sections on separate pages), **Standard** (your config's dashboard,
the layout described above), the **Hero grid** (the same cards as equal tiles),
the **Carousel** and **Widgets**.
Standard and the Hero grid are fixed. The Carousel and Widgets show your own
list of tiles, which you arrange on the device; how it is stored is in
[CONFIGURATION.md](CONFIGURATION.md#home-tiles-hometiles).

The **Carousel** is one row of big cards: the focused card in the middle, its
neighbours smaller on either side, wrapping around at the ends. The focused
card's title and description sit under it, over a dot for each card (with
more than 15 cards, a count such as "7 / 24").

**Widgets** is a grid of square cells, seven across, and each tile covers as
many of them as you make it: from one cell up to the grid's width and three
rows (Continue from 2 × 2, Media from 2 × 1). A tile is a Home card, any
Start-menu entry (a screen, a tool, a device action, a plugin's button) or an
info widget. Three rows fit on the screen and the grid scrolls past them.

| Widget | Shows | A opens |
|--------|-------|---------|
| Clock | The time and the day; wide, also the full date and the next alarm | The Alarm screen |
| Weather | Today's high and low and the sky; wide, also the next days | The forecast, or the Weather switch in Settings while it is off |
| Battery | The charge, and whether it is charging | Quick Settings |
| Storage | The free space on the drive JukaHub runs from | The disk-space screen, or Files |
| Now playing | What is playing | The player (the music player when nothing plays) |
| Calendar | Today's date; tall, the whole month | The calendar |
| Playtime | Time played today and trophies earned | Playtime & achievements |

| Pad | Keyboard | Carousel | Widgets |
|-----|----------|----------|---------|
| Left / Right | Arrow keys | Previous / next card | The tile beside |
| Up / Down | Arrow keys | Up to Search (or the PS4 / PS5 pill or the pairing QR); Down to the power pill; Down from the header back to the card | The tile above / below, scrolled into view; Up from the top row reaches the header, Down from the bottom row the footer pills |
| A | Enter | Open the card | Open the tile |
| L2 or X | Q or X | Edit Home | Edit Home |
| Y | **/** (F2 anywhere) | Search | Search |
| Mouse | — | Click a side card to bring it to the middle, the middle card to open it; the wheel turns one card | Click a tile to open it; the wheel scrolls a row, and a focused tile it scrolls away hands the focus to one still shown (pointing alone never scrolls) |

#### The power card

The power pill at the left end of Home's footer opens a small card with
**Exit** focused. On a PC it holds **Cancel** and **Exit**. On the handheld a
row of **Sleep**, **Restart** and **Power off** sits above them: Left and Right
walk every button, Up and Down move between the rows, A chooses, B closes.
Every button but Cancel ignores presses for the first 0.3 s, so a double press
of the pill cannot act unseen. Restart and Power off quit JukaHub the normal way
first (games stopped, settings, playtime and saves written, the card synced),
then restart or switch off the device. Sleep suspends the handheld with the
screen off; JukaHub carries on where it was when it wakes. Settings → Power →
**Auto sleep** does the same after 3 to 30 minutes without input (off by
default; never while charging, while a game, music, a video, a download or
Screen Share runs, or while an alarm is set).

Settings → Power & battery → **Ask before power off** (on by default) decides whether the two shortcuts that
only exist to switch off ask first: the **Power** tile on Home opens this card
on Power off, and holding the power button opens it. Turned off, both power off
at once — still the clean way, with a running RetroArch game saved first. The
card itself stays wherever it is a choice (the power pill, Quick Settings →
Power, the lock screen), and holding the key in a game does nothing new.

A handheld left asleep for long powers itself off safely: after Settings →
Power & battery → **Power off after sleeping** (6 h by default, 1 h on a
kernel that only light-sleeps) it wakes with the screen off, saves the
RetroArch game it was holding (save state, then quit; only RetroArch's exit
counts as proof) and switches off. On a charger, or with a game it cannot save
that way, it sleeps on instead. At the next power-on **Resume game at power
on** skips the boot animation and starts that game where it was; hold **B** to
skip. While music plays (and no game runs), the power button only turns the
screen off and the music keeps playing (**Power button while music plays**:
Screen off or Sleep); see [MUSIC_PLAYER.md](MUSIC_PLAYER.md#screen-off).

When a game closes within seconds of starting, or cannot be started at all, a card says what is missing and where to copy it, and shows the bug report it can send. **Submit report** is focused, so **A** files the report as a GitHub issue; **B** (or **Cancel**) closes the card and sends nothing. **Left/Right** choose between the two, **Up/Down** scroll and **L1/R1** turn a page; on a PC Enter, Esc, the wheel and a click work too ([Bug reports](BUG_REPORTS.md#when-a-game-or-an-emulator-does-not-start)). The emulators on the Emulators page get the same card when one cannot open. The game options sheet has **Check why it won't start** to run the same check before playing; that card reports nothing, and any button dismisses it.

#### Edit Home

There are six ways in:

- **L2** (the **Q** key) on Home, in any layout; L2 again is Done;
- **X** (the **X** key) on Home while the Carousel or Widgets is showing;
- the **Edit** pill beside the power pill in Home's footer;
- a right-click on a tile, which starts editing on that tile;
- **Start → Device → Edit Home**;
- **Settings → Theme & look → Edit Home**.

The last two work from any screen: they open Home first. On Standard or the
Hero grid every way in switches to Widgets (a message says so), and leaving
without changing anything puts the layout you had back, so a stray L2 costs
nothing.

While editing, the greeting and the search pill give way to a strip:
**Edit Home**, a chip for each layout (the one in force tinted), **Top bar**,
**Reset** and **Done**; the PS4 / PS5 pills and the pairing QR step aside. Every tile wears
a remove badge (a minus) on its top-right corner, and on Widgets the focused
tile shows its size ("2×1") on a tab in the middle of its bottom edge and a
resize handle (the expand arrows) on its bottom-right corner. An
**Add** tile with a plus ends the list (Home holds up to 64 tiles).

| Pad | Keyboard | In edit mode |
|-----|----------|--------------|
| D-pad | Arrow keys | Move between the tiles and up to the strip (Up from the strip: the Top bar row). Holding a tile, move it: Left / Right one place earlier / later, Up / Down to the place of the tile above / below (Down past the end makes it the last) |
| A | Enter | Pick the tile up, or put it down. On **Add**: the list of tiles to add. On the strip: that chip |
| X | X or R | Resize (Widgets only): the D-pad then grows the tile (Right a column, Down a row) or shrinks it (Left, Up) a cell at a time, the grid making room as it goes; **A**, **B** or **X** finish. The tab and the handle turn the accent colour while resizing |
| Y | Delete or Backspace | Remove the tile (**Add** stays) |
| L1 / R1 | Shift+Tab / Tab | The previous / next layout: Standard, Hero grid, Carousel, Widgets |
| B | Esc | Holding a tile: put it down where it is. Resizing: keep the size. Otherwise: Done |
| Start | F10 | The Start menu, which ends editing |
| Select | — | Nothing on its own, so the Select + L1 (virtual mouse) and Select + R1 (control profile) chords still work |
| L2 | Q | Done (as **B**) |
| R2 | E | Nothing: Home does not page away mid-edit |

Leaving Home ends editing too. Every finished change (a drop, a resize, a
removal, an addition, a reset) is saved straight away, and again when editing
ends. The footer reads **D-Pad Navigate · A Move · X Resize · Y / Del Remove ·
L1/R1 / Tab Layout · B Done** (no Resize on the Carousel), while a tile is
held **D-Pad Move · A Drop · X Resize · B Drop**, and while resizing **D-Pad
Resize · A / B Done**.

**Reset** puts the default tiles back: the first press arms it ("Press
again"), a second press within three seconds resets. Choosing Standard or the
Hero grid on the strip shows that layout veiled behind the strip, with
nothing to arrange: the focus stays on the strip until you pick the Carousel
or Widgets, or Done.

**Arranging the top bar.** **Up** from the strip, or **A** on its **Top bar**
chip, swaps the strip for the **Top bar** row: one square chip per control on
the right of the top bar (brightness, volume, Bluetooth, Wi-Fi, weather,
clock, battery, now playing, the update offer, Trophies, Report a bug and
Notifications) in their order, then **Reset** and **Done**. The control the
focus is on is ringed in the live bar above, and the line beside **Edit Home**
names it.

| Pad | Keyboard | On the Top bar row |
|-----|----------|--------------------|
| Left / Right | Left / Right | Move between the chips. Holding a control: carry it one place left / right; the bar follows at once |
| A | Enter | Pick the control up, or put it down. On **Reset** / **Done**: that chip |
| Y | Delete or Backspace | Hide the control, or show it again (a hidden one is faded and struck through; Notifications cannot be hidden) |
| X | X or R | Reset: the shipped order with everything shown, on a second press within three seconds |
| B / Down | Esc / Down | Holding a control: put it down. Otherwise back to the strip |
| L2 | Q | Done |

The footer reads **D-Pad Choose · A Move · Y / Del Hide/Show · X Reset · B
Back**, and while a control is held **D-Pad Move · A Drop · B Drop**. The
order is saved when a control is put down, hidden or shown, on a reset and
when the row closes, and it holds on every screen's top bar. With a mouse:
click a chip to focus it, drag it along the row to carry it, and click the
badge on the focused chip's corner (a minus, or a plus on a hidden one) to
hide or show it. A crowded bar still gives way the usual way: the forecast and
the Wi-Fi meter first, the clock and the battery last.

**Adding a tile.** **A** on the **Add** tile opens **Add to Home**, a card
over Home with four sections (Quick controls, Widgets, Home cards, Apps and
tools) listing what is not on Home yet, each row with its default size.
**Up** / **Down** move, **L1** / **R1** (PgUp / PgDn) jump between sections,
**A** (Enter) adds the highlighted entry, closes the card and puts the focus on
the new tile, and **B** (Esc) closes the card; the mouse and the wheel work too.

**Quick controls** pin the everyday switches. **Wi-Fi** and
**Bluetooth** switch the radio on or off with **A** (Enter): the round badge is
lit while the radio is on, and the tile names the network, or how many devices
are connected. **Power** opens the power card — Sleep, Restart and Power off on
the handheld, with Power off focused; Exit on a desktop. Where JukaHub cannot
switch the radio itself (Windows, macOS) the Wi-Fi and Bluetooth tiles open
their pages instead.

**With a mouse**: click a tile to focus it and its remove badge to remove it.
On the focused tile, drag the handle on its bottom-right corner to any size (it
snaps to whole cells), click the handle to resize with the arrow keys, or
click the size tab to step through 1×1, 2×1, 2×2, 3×2, 4×2 and 3×3. Press on a tile and drag (12 px or
more) to carry it: the tiles make room as it passes over them, and holding it
at the top or bottom edge of the grid scrolls the grid a row at a time. On the
Carousel, drag the middle card sideways: every half card of travel moves it
one place. Click a strip chip to choose it, and the strip's **Done** to
finish. The wheel and a right-click only reach Home when nothing (a menu, a
popover, a panel) is open over it.

### Quick Settings

**Select** on Home opens Quick Settings; so do **F2** on a Brick (unless Settings → Controls → F2 key says otherwise), **Start →
Device → Quick Settings** and **Ctrl+Q**. **Select**, **B** or a click outside
the card closes it. It has three layouts, which **L2** switches (below):

- **Tiles** (the default): a four-column grid of tiles. A switch that is on
  lights up in its own colour, **Volume** and **Brightness** show their level
  as a bar, and the mode rows (Power Mode, Lock screen, Spatial audio, LED
  Mode, Experience, Auto power off) show their value, in the accent while a
  named mode is in force;
- **Compact**: a six-column grid of icon buttons that fits everything on one
  screen; the line under the title names the focused button and its state;
- **List**: one row per button, the panel's old look.

| Pad | Keyboard | In Quick Settings |
|-----|----------|-------------------|
| D-pad | Arrow keys | Move between the buttons. On the List, Left / Right adjust the focused slider or mode row |
| A | Enter | Switch, open, or step a mode row to its next value. On a Tiles or Compact slider: start adjusting it — Left / Right then move the level, and **A** or **B** stop |
| L1 / R1 | PgUp / PgDn | ±5 on a focused slider; on Tiles and Compact the previous / next value of a focused mode row (and nothing elsewhere, so a run of presses never lands on a setting and changes it); on the List, five rows back / forward |
| L2 | Q or Home | [Edit Quick Settings](#edit-quick-settings) |
| R2 | End | The last button |
| B, Select | Esc | Close (**B** first stops adjusting a slider) |

With a mouse, click a button to use it, or a slider's bar to set the level
there (a click elsewhere on a slider focuses it and, on Tiles and Compact,
starts adjusting it); the wheel moves between the buttons, and the pencil in
the title's corner opens Edit. **Power** opens the power card —
Sleep, Restart and Power off on the handheld, Exit on a desktop — from any
screen (JukaHub goes Home first). **Stopwatch** opens the Stopwatch and, while
a stopwatch or countdown runs, shows its time.

#### Edit Quick Settings

**L2** (**Q**) in the panel swaps the title for a strip: a chip for each
layout (the one in force tinted), **Reset** and **Done**. The keys are
[Edit Home](#edit-home)'s:

| Pad | Keyboard | In edit mode |
|-----|----------|--------------|
| D-pad | Arrow keys | Move between the buttons and up to the strip. Holding a button, move it: Left / Right one place earlier / later, Up / Down to the place of the button above / below |
| A | Enter | Pick the button up, or put it down. On the strip: that chip |
| X | X or R | Resize (Tiles and Compact): the D-pad grows the tile (Right a column, Down a row) or shrinks it (Left, Up) a cell at a time, from one cell (two across for Volume and Brightness) to the grid's width and two rows; **A**, **B** or **X** finish |
| Y | Y, Delete or Backspace | Hide the button, or show a hidden one again. Hidden buttons stay on the grid, faded, while editing, and are gone outside it |
| L1 / R1 | Shift+Tab / Tab, PgUp / PgDn | The previous / next layout: Tiles, Compact, List |
| B | Esc | Holding a button: put it down. Resizing: keep the size. Otherwise: Done |
| Select | — | Nothing on its own, so the Select chords still work |
| L2 | Q or Home | Done |

The focused button wears a hide badge in its top-right corner (a minus; a
plus on a hidden one) and, on Tiles and Compact, its size ("2×1") on a tab at
its bottom edge; the line under the strip names it. The order and the hidden
buttons are shared by the three layouts, the sizes by the two grids (a tile
wider than the Tiles grid fills its width there). Every finished change is
saved at once. **Reset**, pressed twice within three seconds, puts the
default buttons back and keeps the layout. With a mouse: click a strip chip to
press it; click a button to focus it and again to pick it up, then click the
button whose place it should take; click the badge to hide or show it, and
the size tab to step through 1×1, 2×1, 3×1, 2×2, 4×1, 6×1, 4×2 and 6×2.

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

**Movies & TV** (the second chip): **A** plays a film or opens a list (a show,
a season, a network server's folder, a whole free-film collection), **B** goes
back up one level, **Y** opens the details (resume, play from the start, Up
Next, Favorites) and **R2** loads more of a long collection. See
[Movies & TV](MOVIES_TV.md).

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

The gallery of theme presets (Settings → Theme & look → Theme presets) has
three tabs: **Colour**, **Styles** and **Experience**. A colour preset repaints
the interface without changing your Home layout. Styles holds distinctive
looks that also change the Home arrangement (PS1 to PS5, Super Nintendo to
Switch, Mega Drive, Dreamcast, Xbox and Minimal UI). Experience opens the
Experience packs screen, which shows the
same tabs; there L1 / R1 switch tabs too and the D-pad moves between packs, and
B leaves to wherever the presets were opened from.

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

The page fills the screen, and **Select** opens the menu that holds every
action: Back, Forward, Reload and Home across its top, then the address bar,
web search, the page's links, find, zoom, link mode, saving, opening the page in
a full browser, site info, the ad filter, sign-ins, the layout switch, Browser
settings and Close (see
[docs/BROWSER.md](BROWSER.md#full-screen-and-the-select-menu)). In the menu
Up/Down move and wrap, Left/Right change **Zoom** (and step a running **Find**),
L1/R1 jump five rows, **A** acts and **B** or Select closes. On a keyboard **M**
or the Menu key opens it; with a mouse, a right click.

| Button | Action |
|--------|--------|
| Select (keyboard **M** or the Menu key, mouse right click) | Open the browser menu; Select or B closes it |
| Up / Down | In scroll mode, scroll the page (in the classic layout, at the top/bottom the focus goes to the header and the button row). In link mode, move to the nearest link on the row above/below |
| L1 / R1 | Scroll by a page |
| L2 / R2 (keyboard **-** / **+**, or the toolbar **zoom** chip) | Zoom the page text out / in (75%–160%); the chip shows the scale, lights up off 100%, and steps up on a tap |
| Left / Right | Walk the links in reading order, scrolling them into view |
| **Tab** (keyboard), or **Select** → **Link mode** | Switch between scrolling the page and walking its links (**B** goes back to scrolling) |
| A | Open the highlighted link, work the highlighted form control (type into a field, step a list, tick a box, send a form), or edit the address bar when nothing is picked |
| Y (keyboard **Y** or **/**) | Search the web (opens the keyboard); the **SEARCH** chip does the same |
| X | Open the full link list for the page |
| B | Leave the browser (in link mode, the first press returns to scroll mode) |
| `f` · **Find** chip | Find on the page; `n` / `p` step to the next / previous match, **Esc** clears the search |
| **Save** / **Marks** chips | Save the page on screen / open the saved-page list, whose chip carries its count |
| Y in **Saved pages** | Make the highlighted page your home page |
| **Home** chip | Open the home page; Settings → **Browser** chooses it, and the engine **Y** searches with |
| Backspace or Alt+← / Alt+→ · F5 (keyboard) | Back / Forward · Reload |

Links are outlined as they come into focus, so a link deep in an article can be
followed without opening the picker. Walking them needs the D-pad to do two
jobs, so **Tab** on a keyboard, or a click on the left stick on a real gamepad,
cycles scroll mode and link mode; the toast names the keys. In link mode
Up/Down follow the page's rows and pick the nearest column, so a two-column
layout does not throw the cursor across the screen, while Left/Right stay in
plain reading order. **Y** (or the **SEARCH** chip beside the address bar) opens
the keyboard on an empty field: type a phrase to search, or a host to open it.
The search goes to the engine chosen in Settings → **Browser**, DuckDuckGo Lite
unless you change it (see [docs/BROWSER.md](BROWSER.md#search-engine-and-home-page)).
In the classic layout (Settings → **Browser** → **Full-screen pages** off) the
**Back**, **Forward**, **Reload**, **Links**, **Filter**, **Search**,
**Home**, **Issues**, **Sites**, **Forget**, **Save**, **Marks** and **Find**
chips at the bottom are ordinary scene buttons, reachable by pressing Down past
the end of the page and Up to come back. **Sites** lists the hosts whose cookies
are on the card so one can be signed out on its own, and **Forget** drops them
all, so a page it is signed in to starts signed out; it asks twice, and the first
press says how many would go (see
[docs/BROWSER.md](BROWSER.md#forgetting-them)). **Save** keeps the page on
screen for later and **Marks** opens that list (see
[docs/BROWSER.md](BROWSER.md#saved-pages)).

In that layout the chip at the right end of the address bar reports how many ad and tracker
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
button, and the ring glides from button to button (Settings → Focus bounce).
With a mouse, the level bar beside the speaker sets the volume, and the seek
row takes a click anywhere along the track or a drag: the handle follows the
pointer with its time over it, and the video jumps there when you let go. A
pointer resting on the track shows the time under it. A click on the picture
plays or pauses, a double click goes full screen, and the wheel over the
picture is the volume.

While a video plays, the controls step aside three seconds after the last
press, in the Media screen's window as well as in full screen, so the picture
is clear; they fade back on any press, when it pauses, during a seek, and when
the pointer moves over the picture. A first **Left** or **Right** while they
are away only brings them back, with the ring on play/pause, so the ring never
moves where you cannot see it; **A** plays and pauses straight away. With
Reduce motion on they fade without sliding, and Battery Saver shows and hides
them at once.

The corners are two separate actions: **Close** (the X) ends playback and leaves
the video, and the downward chevron beside it steps the player down a size without
ending it — the same thing **B** does, and the way to reach the floating window
from a pad. On a touch screen the windowed player's controls take taps now, and
a tap anywhere else on the scene still belongs to the scene.

| Button | Action |
|--------|--------|
| Left / Right | Move the selection along the control bar (the first press while the controls are away only shows them) |
| A | Press the selected control (play/pause when nothing is selected, or while the controls are away) |
| Up / Down | Volume, shown by a level pill at the side of the picture; the rest of the controls stay as they are |
| X (keyboard **X** or **C**) | Subtitles on / off (in your subtitle language; CC stays on for the next video) |
| Y (keyboard **Y** or **S**) | Video settings sheet |
| L1 / R1 (keyboard **J** / **L**) | Skip −10 / +10 seconds; quick presses add up (**+30 s**) and the video jumps once you stop |
| K / M (keyboard) | Play / pause · mute |
| L2 / R2 | Previous / next video in the list that launched this one (search results or the Shorts feed) |
| B | Step back from the settings sheet's Save offline page, close the sheet, then step down: leave full screen, float the video and go back; press again to close the floating video |

The rewind and fast-forward buttons skip 10 seconds per press. Presses in quick
succession add up: the handle and the clock move to the new place at once, the
time shows in a bubble over the handle and "+30 s" on the picture, and the
video starts again there once, a moment after the last press — not once per
press. While it gets there, a spinner sits on the last picture. With the
settings sheet open, **Up / Down** move between its rows and **A** applies the
row, so subtitles, full screen, speed and quality are reachable without a touch
screen. A tap on a control also moves the ring there, so touch and D-pad never
disagree.

The volume a video is left at carries on to the next one; a muted video's next
one starts at the level from before the mute.

**Audio track and subtitles** — for a video on the card the sheet has an
**Audio track** row: **A** steps to the file's next audio stream ("2/3 · English ·
AC3 5.1") and the video carries on from where it was, as after a seek. Its
**Subtitles** row steps through **Off**, the subtitle files beside the video
(`.srt`, `.vtt`, `.ass` / `.ssa`, MicroDVD `.sub`, also with a language such as
`Film.en.srt`) and the text tracks inside it, when there is more than one; with
one or none it is the plain on / off switch. Picture subtitles (Blu-ray PGS, DVD)
cannot be drawn and are skipped. Both choices are remembered for the file
(`video-tracks.json`), so the film comes back in the same language. **Up / Down**
stay the volume during playback; the tracks change from the sheet.

**Speed** — the pill in the bar, the sheet's **Speed** row, or keys **1**–**4**
(0.5× / 1× / 1.5× / 2×) — steps 0.5× → 0.75× → 1× → 1.25× → 1.5× → 2× and back,
and the video really plays at it: picture and sound start again together where
you were (a moment's pause, like a seek), the sound keeps its pitch, and the time,
seek bar, subtitles and resume point move at the new rate. A paused video stays
paused. The speed stays while the same video plays — seeks, a quality change, the
download that takes over a broken stream — and carries to the next video when the
player moves on by itself or from its own buttons (L2 / R2, previous / next,
Up Next, Shorts autoplay); a video opened from a screen starts at 1×. A live
stream and a Watch Party stay at 1×, and say so.

**Save offline** opens a second page in the same sheet: **Download video** (MP4)
and **Audio only** (M4A) for a web video; **Extract audio** and **Get subtitles**
(from the page a download came from) for a file on the card. **B** goes back to
the settings, then closes the sheet. Saved files are in Media → Downloads, and
tracks are in the music player too. A downloaded video keeps its subtitles beside
it as `<name>.<lang>.srt`, so CC works offline.

**X** fetches subtitles in your language (Settings → subtitle language): a
written track, another region of it, the auto-generated track or a translation
YouTube makes, from YouTube's caption list first and the YouTube helper second.
They stay on for the next video until you turn them off; a YouTube video then
brings its subtitles up by itself, anything else waits for a press of **X**.

The **Quality** row cycles Auto → 360p → 480p → 720p → 1080p for the video on
screen only — Auto means the global *Playback resolution* setting. Picking a
quality re-resolves the stream and resumes playback at the current position, and
the pin is forgotten when another video starts. The re-resolve is a modal wait
like starting a video: the loading card names the source, the scene underneath
stops taking input, and **B** aborts it — the video that was already playing
keeps playing, because nothing is torn down until the new stream is ready.

When a video will not stream and has to be downloaded first, the loading card
shows the download's progress and a second button: **Y** (the Y key, or a tap
on the pill) *Download in background*. The card closes, everything works again,
and the download carries on with a chip at the top right showing its progress
(its ✕ stops it). When it is done it is saved to **Media → Downloads**, a
notification says it is ready, and the chip turns into *Ready* — click it (or
use the virtual cursor) to play it from where you pressed Play. It never starts
playing by itself. Two downloads can run in the background at once; pressing
Play on a video that is still downloading says how far along it is.

On a keyboard **N** and **P** move to the next and previous video of the list
that launched the current one — in the Shorts feed that is the next short. In
the feed the four videos after the one playing are resolved in the background,
so skipping does not wait for yt-dlp.

**Back steps down the video.** In full screen it
returns to the scene's player, and in the scene's player it shrinks the picture
into a small window in the bottom-right corner and opens the previous screen.
The session keeps playing — position, subtitles and audio are untouched — so
the Media grid, Home or any other screen is usable while the video runs. Press
**B** again (or **Esc** on a keyboard) to close the floating video without
moving focus. Tap the window to return to the player; its **×** also ends the
session, as do the player's Stop button and `q`. The dock and the screensaver
cover the window because they take the whole panel.

Progress, resume position and subtitles are handled automatically; the
subtitles ride up and down with the control bar. In the last ten seconds of a
video with something in Up Next, a card names what comes next and counts down.
When a video reaches its end the player closes and returns to the scene — the
frozen last frame no longer keeps the grid behind it locked.

## Chat

The Chat screen has three conversation tabs: the community channel (the
JukaHub Discord, when it is configured), **Juka AI**, and **Saved** for notes
that stay on this device. A fourth tab, **Voice**, is the live voice room
([Voice](VOICE_CHANNEL.md)): clicking it joins, and L1 / R1 onto it open it
with Join selected. Every speaker can be opened as a profile card with their picture,
handle, roles and last message.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Walk the history one message at a time (a tall message scrolls first); Down past the newest message reaches the files waiting to be sent, then the composer |
| D-pad Left / Right, L1 / R1 | Previous / next tab; on the waiting files, Left / Right picks one; on the composer, Left selects the paperclip (Right goes back) |
| L2 / R2 | Jump to the oldest / newest message |
| Right stick | Up / down scrolls the history freely; tilt it sideways for the pointer (A clicks, D-pad puts it away) |
| A | Type a message; on a selected message, open its actions; on a waiting file, take it off the message |
| Start | Send what is typed and attached (with nothing to send, Start opens the Start menu as everywhere) |
| X | Attach files: Files opens to pick them |
| Microphone beside the paperclip / V | Start or finish a voice clip; the finished clip waits in the attachment tray until Send |
| Y | The tab's own action: **People** in the community tab, **New chat** (or **Add key**) in Juka AI |
| B / Back | Leave the chat; an open card, list or action bar closes first, and on the waiting files B goes back to the composer |

On a keyboard, Tab / Shift+Tab (or 1–3) pick a tab, Enter types straight into
the composer (Enter sends, Shift+Enter starts a new line, Esc stops typing),
Ctrl+Enter sends, **X** attaches files, Del takes the waiting file under the
cursor off the message, and **R** renames you (so do a click on your name at
the top right and **Rename** on your own card in People, which is there before
you have written anything).

For live voice with everyone, open the **Voice** tab (click it, press **4**, or
step onto it with L1 / R1 and press **A**). For a private call, open
**People**, mark one or more names with **X** or click the circle beside a
name, then press **Y** or **Voice Channel**. A Voice Channel invite appears in
the community conversation; select it and choose **Join Voice** to join from
another JukaHub. The call stays active when you return to Chat. See
[Voice](VOICE_CHANNEL.md) for call controls, the free voice server and joining
from a phone.

### Sending files

**X** (or the paperclip at the start of the composer) opens Files in pick
mode. The paperclip is selectable: **Left** on the composer steps onto it,
**A** there opens the picker, and **Right** or **B** goes back to the text
field. Files' bar says which tab the files are for; its bar says which tab the files are for. **A** on a file attaches it and
brings the chat back; mark several with **X** first to attach them together.
Folders open as usual and **Left** climbs one, while **B** calls the pick off
and goes straight back to the chat. **Send to chat** in Files' **Y** sheet
attaches the marked rows from any visit to Files (to the community tab, unless
Chat is picking for another one), and on a PC a file dropped on the window
while the chat is open is attached too.

Attached files wait in a row above the composer until the message goes; words
are optional. **Up** from the composer reaches them, **Left / Right** picks
one, and **A**, Del or a click on its **×** takes it off. Where they go depends
on the tab:

- **Community** — to the Discord channel with the message, up to 10 files of
  up to 20 MB each (50 or 100 MB on a server boosted to level 2 or 3). The line
  under the bubble shows how far the upload is, and messages written after it
  wait their turn; a dropped connection waits and tries again without posting
  twice, **Cancel** in the message's actions stops it (deleting it while it
  goes out takes it back off the channel), and a file Discord refuses says why
  under the bubble, with **Retry**.
- **Juka AI** — text files (logs, configs, notes), pictures (JPEG, PNG, GIF,
  WebP, BMP), videos and recordings, up to three with a question. Juka is shown
  a text file (the start and the end of a long one) and sees a picture; a
  video is shown as three frames with what is said in its first ten minutes,
  and a recording is heard. A follow-up keeps them, and a file sent alone asks
  what is in it. HEIC and other formats JukaHub cannot open are refused when
  attached. Videos need ffmpeg (installed like the player's when it is
  missing).
- **Saved** — any file, kept as a reference to where it is on this device.

Files other people send show under their message. The first picture is drawn
in the bubble; every other file is a card with its name, its size and what a
click on it does: **View** a picture or **Play** a clip or a song straight from
the chat, and **Open** anything else — it is saved into Downloads (your
Downloads folder on a PC, `Downloads` at the top of the SD card on the
handheld) and opened from there, in Files when JukaHub has no viewer of its own
for it. In a message's actions, a single file has the same **View**, **Play**
or **Open** (a lone picture: **Enlarge**), several are listed by **Files**, and
**Download** saves what is not on this device yet. Discord's file links expire
after about a day; an older one is renewed when it is shown or opened.

**Names and pictures are the way to a profile**: click a speaker's name or
avatar in a bubble (or use **Actions → Profile** on a selected message). The
card offers **Mention**, **Latest** (jump to their newest message), **Copy ID**
and **Picture** (open the full image in the viewer); tapping the card's picture
does the same. The **N people** chip in
the header (or **Y**) opens the list of everyone in the history, so a name that has
scrolled away is still one press away.

A selected message turns the composer into its action bar: **Reply** (a quote
header in the bubble, sent as a real Discord reply), **Copy**, **Ask Juka**,
**Save**, **Profile**, and **Delete** for your own messages; when they do not
all fit, the row follows the focused action and "…" marks the rest. A message that carries a
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
| R2 | Voice typing: press to talk, press again to send (**F4** on a keyboard). With the pointer up over the page above the keyboard, R2 scrolls instead |
| B / Back / Start | Close |

It also closes with its own close button, by tapping outside the panel, or when
you press **Done** / **Send**. A tapped key flashes as pressed, a moving mouse
pointer lights up the key it is over before you click, and an empty field keeps
its caret visible so it is obvious where typing will land.

## First-run tour

The first launch opens a short, skippable tour of the D-pad controls, top-bar
clock and notification bell, Appearance settings, and the Start menu. **A** or
**Enter** advances, **Left** goes back, and **B** or **Esc** skips.

Right after the welcome it asks **What should we call you?** **A** (or **X**)
opens the on-screen keyboard on your name; submit saves it, **B** leaves it as
it was, and after that **A** goes on (so does **Right**: the name is optional).
It is the same name as **Settings → General → Your name**: Home's greeting, the
chat and the startup and shutdown animations use it.

The tour then offers a colour palette and asks you to choose a **Home layout**.
All five layouts have small previews: Tabs, Standard, Hero grid, Carousel and
Widgets. Use **Up/Down** to highlight one and **A/Enter** to apply it. The
choice is saved, and Settings → Theme & look → Home layout can change it later.

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
official releases. Whatever JukaHub installed — cores, hakuX, those Windows
emulators — is also kept current without a press by the daily **Emulator
updates** pass (Settings → Games & emulators; Install automatically by
default), which keeps the replaced version for the same roll back.

### Xbox (hakuX on the Smart Pro, xemu on Windows) and Xbox 360 (Windows)

The **Xbox** tile is always on the page. What it offers depends on the device:

| Device | Xbox tile |
|---|---|
| Smart Pro (TG5040) | Installs the experimental [hakuX port](https://github.com/ToastedFrog2/hakux-trimui-smart-pro) |
| Brick Pro | The same install, marked untested, with spruceUI's build of hakuX (which draws at the panel's own 1024x768) in place of the Smart Pro's. An install an older JukaHub made may still have the Smart Pro's: the first Xbox game's start gets this model's build first, once (online), and so does **X** |
| Brick, Hammer | The same, marked untested and never installed by itself; no analog sticks (hakuX's **Menu** maps a game's controls per title, so a stick's job can go on the D-pad) |
| Smart Pro S | The same, marked untested: spruceUI's build runs on any Vulkan driver, the A523's Mali included |
| Windows | xemu, installed from its official release; **Y** on its tile opens the BIOS checklist |

The Xbox 360 is played on Windows only, by Xenia Canary (below). On a handheld
a `Roms/XBOX360` folder shows a **Not supported** tile that says why: Xenia
needs a 64-bit x86 CPU with AVX and a Direct3D 12 or Vulkan GPU.

#### hakuX on the Smart Pro

On the handheld tile, **X** installs or updates hakuX, **A** opens the Xbox
games in the Games Library (before the install, the setup sheet, where **X**
installs too), **Y** opens the setup sheet and **R1** rolls the last update
back, in the background like the install; **L1** includes hakuX when JukaHub
installed it, and the daily **Emulator updates** pass updates it by itself
(never during a game; a version you rolled back from is not put back). While
a download runs, the sheet's subtitle shows its progress.

You do not have to press anything on a Smart Pro: with **Emulator updates** on
*Install automatically* (the default), the daily pass installs hakuX once, by
itself, when the card has no Xbox launcher — at its quiet moment (on the
charger or after two idle minutes, never during a game), online, after a check
that the card keeps 256 MB free besides the download, under the install card
and the tile's *Installing…* pill — and its toast says where the games go.
The card remembers it had an Xbox launcher (`Apps/JukaHub/data/hakux/auto-install.json`),
so a hakuX you delete is not installed again by itself and an `Emus/XBOX` you
made is never replaced. **A** on an Xbox game in the Games Library before hakuX
is there installs it right away and starts the game once it is in (on the
Brick Pro too, which otherwise keeps the install a press).
The install:

1. asks GitHub for the newest release, and for the newest pre-release when no
   release carries the package (an experimental port may publish nothing
   else), and takes its `xbox-trimui-smart-pro-v<version>.zip`; between two
   packages of one version the final release wins over its release candidate
   (`0.1.3` over `0.1.3-rc1`), and an installed final release is never
   "updated" to a candidate;
2. checks the download against GitHub's SHA-256 digest for that file, and
   against a checksum file attached to the release when there is one; the
   download's SHA-256 is recorded either way;
3. unpacks it into `Apps/JukaHub/data/hakux/stage/` on the same card and only
   then merges the package's `Data/` and `Emus/` folders into the card's root,
   file by file. Files no version of the package shipped (saves, shader
   caches, settings) are never touched; a file it replaces is kept in
   `Apps/JukaHub/data/hakux/backup/`, which is what **R1** restores. A file
   the previous version installed and the new one no longer ships goes into
   that kept copy too, if it is still exactly as installed (a changed one is
   hakuX's or yours by now and stays). A merge that fails half way puts back
   everything it moved;
4. writes `Emus/XBOX/.jukahub-hakux.json`: the release, the asset URL, the
   digest, how the download was verified and every file laid down.

A failed install says why in plain words: no connection, GitHub limiting
downloads, a download that was cut off or arrived damaged, a full card, or a
clock so far off that the secure connection fails.

Games go in `Roms/XBOX` as `.iso` (XISO) images and start through the
package's own `Emus/XBOX/launch.sh`. JukaHub closes while an Xbox game
plays, as it does for a PS Remote Play or Moonlight stream: it hands the
screen to a small script and quits, the script runs the launcher exactly as
the stock menu would, and JukaHub's `launch.sh` starts JukaHub again when the
game ends — back in the Games Library on the game when it was started there.
hakuX then has the screen, the buttons and all of the 1 GB to itself; next
to JukaHub its picture could stay hidden behind JukaHub's last screen and
Menu could reach the firmware's quick menu instead. In a game, **Menu**
opens hakuX's settings (per-game settings, LAN, screenshots to
`Screenshots/XBOX`), and holding **Start + Select** for about 1.2 seconds
exits; JukaHub's own in-game chords are not there while it is closed. The
time played counts in the game's playtime, and a game that stops within
seconds opens the launch card with the launcher's last lines
(`Apps/JukaHub/data/gamediag/launcher.log`). A JukaHub started by hand (over
SSH) or under an older `launch.sh` starts the game beside itself as before.

hakuX 1.0 and later run games on their own Xbox kernel and need no BIOS, MCPX
ROM or HDD image; the xemu-based builds before them did, and the port does not
say which core it was built from. So the setup sheet and the BIOS Doctor read
the installed build's own settings and list any firmware file they name, with
whether it is there; when they name none, the sheet says so.

Set expectations accordingly: this is an experimental port on a handheld with
1 GB of RAM. Not every game boots, 30 fps is a target rather than a promise, and
some games show graphics glitches. JukaHub installs the published package;
rebuilding it from the port's source is not automated.

#### xemu on Windows

**A** or **X** on the xemu tile installs xemu from its official GitHub release
(`xemu-project/xemu`): the Windows x64 build, `xemu-<version>-windows-x86_64.zip`
or its older name `xemu-win-x86_64-release.zip`, never the debug build or the
symbols published beside it. A JukaHub built for Windows on ARM takes the ARM64
build and falls back to the x64 one. The install also fetches the free
hard-disk image the xemu project publishes (`xbox_hdd.qcow2` from
`xemu-project/xemu-dashboard`) into `BIOS\xbox` next to JukaHub (in its data
folder when JukaHub's own folder is read-only).

What xemu cannot ship is the Xbox's own firmware: the MCPX boot ROM
(`mcpx_1.0.bin`, 512 bytes) and a flash ROM (BIOS, such as `Complex_4627.bin`)
are your own dumps; JukaHub never downloads them. **Y** on the xemu tile opens
the checklist: what each file is, where it goes, which ones were found (the boot
ROM's MD5 tells a good dump from the known-bad one), and buttons to fetch the
HDD image, open the folder and apply. The sheet looks again every few seconds,
so files dropped into the folder turn green while it is open. **Apply** writes
the files into xemu's own `xemu.toml` (`[sys.files]`); starting xemu from its
tile does the same when the files are there, and the tile says while something
is still missing.

Games go in `Roms\XBOX` as `.iso` or `.xiso` images and start from the Games
Library full screen, with xemu's documented `-full-screen -dvd_path <image>`.
A game started while a file is still missing does not open an xemu window that
cannot boot it: JukaHub says which file is missing and where it goes.

#### Xenia Canary (Xbox 360) on Windows

**A** or **X** on the Xenia Canary tile — or on the tile of a `Roms\XBOX360`
folder that has games but no emulator yet — installs Xenia Canary from its
official releases (`xenia-canary/xenia-canary`). Canary has no version
numbers: every build is its own release, tagged after its commit
(`7a7b089_canary_experimental`) and marked Latest, and its Windows build is
`xenia_canary_windows.7z` (older builds were `.zip`), unpacked with 7-Zip like
PCSX2. A newer build shows as an update like any other emulator's, and **R1**
puts the previous one back.

Xenia needs no console files, but it does need a 64-bit x86 CPU with AVX, a
Direct3D 12 or Vulkan GPU and the Microsoft Visual C++ Redistributable (x64);
the tile and the install toast say when that runtime is missing. Games go in
`Roms\XBOX360` as `.iso` images, which the Games Library lists under
**Xbox 360** and starts with `xenia_canary.exe "<game>" --fullscreen=true`.

#### Azahar (Nintendo 3DS) on Windows

3DS games (`Roms\3DS`, also `N3DS`) play in
[Azahar](https://github.com/azahar-emu/azahar/releases), the maintained 3DS
emulator that took over from Citra and Lime3DS. **A** or **X** on its tile (or
on the tile of a `Roms\3DS` folder that has games) installs the portable build
from Azahar's official releases, `azahar-windows-msvc-<version>.zip`
(`azahar-<version>-windows-msvc.zip` up to 2124.3), never the installers, the
msys2 or mxe builds or the libretro core published beside it; a Windows on ARM
PC takes the same x64 build. A newer release shows as an update like any other
emulator's, and **R1** puts the previous one back. JukaHub also finds a copy
you installed yourself (Program Files, or your user's Programs folder), a
portable copy in a folder named `Azahar` (the
Emulators folder, Desktop, Downloads, Documents, Games) or `azahar.exe` on
PATH, and starts a game with `azahar.exe -f "<game>"` (full screen). The shelf
lists `.3ds`/`.cci`, `.cxi`/`.app`, `.3dsx` and `.elf`/`.axf` (and their
compressed `.z…` forms). A `.cia` is an installer: install it with Azahar's
**File → Install CIA**. On a handheld, a `Roms/3DS` folder shows **Not
supported** and says why.

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
**Emulators**. The panel lists the Windows programs it found and starts them:
through Box64 and Wine inside WestonPack on the handheld, directly on Windows;
see [WINDOWS_APPS.md](WINDOWS_APPS.md).

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through apps |
| A | Start the selected app; its row shows the progress (the first start prepares Windows for a few minutes). **Wine Test (built in)** starts the same way: one window that shows Wine works. On an empty list (handheld), download the sample program, Notepad++ |
| Y | Display for this app: Windows (GDI) or OpenGL (handheld). While the list holds only the Wine test, download the sample program instead |
| X | The runtime setup page (handheld) |
| Start | Rescan the app folders |
| B | Close setup, then the launcher |

On a keyboard, **Enter** starts, **Y** or **D** switches the display, **X** or
**H** opens setup, **R** or **F5** rescans and **Esc** goes back. Tapping a row
starts it (on an empty list, tapping the sample line downloads it); tapping
outside closes the panel.

While a Windows program runs on the handheld, the pad is its mouse and types a
few keys:

| Button | In the Windows program |
|--------|------------------------|
| Either stick | Move the pointer |
| D-pad | Arrow keys (on the Brick, which has no sticks, the pointer) |
| A / B / X | Left / right / middle click (hold A to drag) |
| L2 / R2 | Wheel up / down |
| Y | Esc |
| Start | Enter |
| L1 / R1 | Tab / Space |
| Select + Start | Alt+F4 (close the window); held 2 s, end the program |
| Select + L1 (hold), Menu | Give the pad to the program as a gamepad (Select + L1 again takes it back) |

## PortMaster ports

Open **Start → PortMaster Ports**, or the pinned **PortMaster Ports** row in
**Emulators**. On a fresh card, **A** installs PortMaster's official TrimUI
package into `Apps/PortMaster`. Afterwards the panel lists PortMaster first,
then every port launcher in `Ports/`, `Roms/Ports`, `Roms/PORTS` and
`Data/ports`. See [PORTMASTER.md](PORTMASTER.md) for paths and limitations.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through the rows |
| A | Install PortMaster, launch an installed port, or download the selected catalogue port and hand it to PortMaster to install with its runtimes |
| X / Tab | Switch between installed ports and PortMaster's catalogue |
| Y / F | Toggle the **May Run Here** device filter in the catalogue |
| R1 / S | Search the catalogue |
| B | Back (to Emulators when opened there) |

Tapping a row activates it; tapping outside closes the panel.

## RomM server

Open **Tools → RomM server**. Under **Connection**, enter your server address
and Client API Token (or choose **From your phone** and type both on the Phone
Companion's **RomM server** card), then choose **Connect and load games**. The **Games** tab
downloads a single-file game and cover; an installed game also offers **Sync
saves**. The **BIOS files** tab downloads firmware to the card. See
[ROMM_SERVER.md](ROMM_SERVER.md) for save conflict handling and multi-file games.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through tabs and files |
| A | Open a tab, game actions, or a BIOS download |
| X | Go to the next tab |
| Y | Refresh the active list |
| B | Back to Tools |

## Packages

The Packages store shows the package list as shelves (the default), a grid or a
list. **A** opens a package's sheet, **X** (or `r`, **F5**) refreshes the list,
**Y** (or `v`, `1`–`3`) switches the view, **L1** / **R1** page (and step
between packages while a sheet is open; `[` / `]` on a keyboard), **L2** /
**R2** jump to the first and last row on other devices or in a package sheet,
and in the grid and the list `s` changes
the sort and **Tab** walks the filter chips; **B** closes the sheet, then leaves.

An installed package that can start on this device leads its sheet and the
banner with **Run** (**Play** for a game), which starts it the way the Apps
screen does; **Open folder** follows it. Installed apps are listed on the Apps
screen too ([PACKAGES.md](PACKAGES.md#the-packages-store-jukalang-registry)).

Under the toolbar, in every view and even while the list is empty or offline,
the **More stores** strip opens the [APK Store](#apk-store), PortMaster's
ports (on the handheld), and Windows Apps. **Up** from the top row of a view (or
from the grid's chips) moves onto the strip, **Left** / **Right** pick a store
and **A** opens it; **Up** again reaches the top bar and **Down** goes back to
the packages. Back from the APK Store returns to its pill. A click or tap on a
pill opens it.

On the **TrimUI Smart Pro**, **L2** / **R2** move backward / forward through
**Packages → APK Store → PortMaster → Windows Apps**, wrapping at either end.
The triggers work from each destination opened through Packages; a details
sheet keeps its own controls until it is closed.

## App Center

**Apps → App Center** (or Home search "app center") lists TrimUI apps with
where they come from, how they fare on the stock firmware and on CrossMix, and
what they need ([APP_CENTER.md](APP_CENTER.md)).

| Pad | Keyboard | Action |
|-----|----------|--------|
| D-pad | Arrows | Move |
| A | Enter | Open the app's sheet (on **Releases → Look for new releases**: check now) |
| X | X | Start the focused installed app |
| Y | Y | Look for new releases (at most once a minute) |
| B | Esc | Back |

In an app's sheet **Left / Right** choose a button (Get, Update, Open, Roll
back, Remove), **A** / Enter presses it, **Up / Down** scroll, **Y** enlarges the
source's QR code and **B** / Esc closes. A mouse clicks rows and buttons and
the wheel scrolls.

## APK Store

Open it from **Packages → More stores**, **Start → Games → APK Store** or search
("android", "apk"), including on Windows. It has four tabs: **Your APKs**
(APKs found on the card or PC, in the Phone Companion's `PhoneDrop` folder and
in the download folders, each with a verdict), **Store** (known titles and
kinds of APK, and how to get them legally), **F-Droid** (browse and download
verified APKs), and **Runtime** (AndroidRT, this device and Waydroid). A PC can
inspect and download APKs; AndroidRT runs them on the handheld. See
[ANDROID.md](ANDROID.md#apk-store).

| Button | Action |
|--------|--------|
| D-pad | Move through the tabs and rows |
| A | On an APK: what you can do with it (Add to Games, Play, Remove from Games); elsewhere: open the row |
| X | Everything the store read about the APK or the catalogue entry |
| Y | Read the folders again |
| L1 / R1 | Previous / next tab |
| L2 / R2 | Previous / next Packages destination on a Smart Pro when opened through Packages |
| B | Close a sheet, then go back |

On a keyboard, **Enter** is A, **X** or **Del** shows the details, **Y**
rescans, **Tab** / **Shift+Tab** (or **PgUp** / **PgDn**) switch tabs and
**Esc** goes back. The mouse wheel scrolls and a click picks a row.

## PS Remote Play

Open **Start → PS Remote Play**, or press the **PS4** / **PS5** tile in the
home header. See [REMOTE_PLAY.md](REMOTE_PLAY.md) for pairing, playing away
from home and the files.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through consoles and settings |
| D-pad Left / Right | Change Quality, Video codec, Select button, Rumble or Volume |
| A | Play (waking a resting console first), pair, or run the row |
| X | Wake the selected console without streaming |
| Y / Delete | Forget the selected console |
| Start / O | The selected console's options: internet address, login passcode, forget |
| B | Back from a card or the console options, then close; stops a wake that is waiting |

In a stream the controls are the PlayStation's: a Menu tap is the PS button,
Select the touchpad click (or Create), Menu + D-pad swipes the touchpad,
holding Menu opens the stream menu (its **Hold PS (Home)** row gives the long
PS press) and holding Start + Select on one pad quits. When the console asks
for the login passcode, Up / Down change a digit, Left / Right move, A sends it
and B gives up. The full table is in
[REMOTE_PLAY.md](REMOTE_PLAY.md#controls-in-a-stream).

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

## Everyday apps

Notes, Tasks, Voice Memos, Paint, Stopwatch, the Calculator, Alarms and the
Pomodoro timer are under **Tools → Everyday apps** (also on the Apps screen,
in the Start menu and in Home search); Solitaire is under **Games**. What each
one does is in [Everyday apps](EVERYDAY_APPS.md); these are their keys.

| App | Pad | Keyboard |
|-----|-----|----------|
| Notes | **A** open · **Y** new (the on-screen keyboard opens on its title) · **X** delete (asks first) · **L1 / R1** folder filter · **Select** menu: Search, Every note as a file, Open on phone · **B** back | Enter or Space · N or Y · X or Del · F · M; S search, Ctrl+S every note as a file |
| Notes, a note | **A** type into the title or the body with the on-screen keyboard · **Up / Down** title ↔ body (and scroll the body) · **L1 / R1** move the note to another folder · **Y** or **Select** menu: Save as file, Open on phone, Delete note · **B** save and back. On the on-screen keyboard the action key is **Next** on the title and **New line** on the body; **Start** or **Y** is Done, and closing it saves | type · Enter or Tab title → body · Ctrl+Up body → title · Ctrl+S save · Ctrl+M or the Menu key: the menu · PgUp / PgDn scroll · Esc save and back |
| Tasks | **A** done / not done (a repeating task moves to its next date) · **Y** add · **X** the task's (or list's) sheet · **L1 / R1** previous / next list · **Left / Right** lists ↔ tasks · **B** back | Enter or Space · N or Y · X · PgUp / PgDn · Tab; Del twice deletes |
| Tasks, due picker | **Left / Right** quick pick, ±1 day or ±5 min · **L1 / R1** ±7 days or ±1 hour · **A** use · **Y** save · **B** cancel | arrows · PgUp / PgDn · Enter · Y · Esc |
| Voice Memos | **Y** record · **A** play / pause the focused memo · **X** options (rename, send to phone, delete, microphone test) · **L1 / R1** or **Left / Right** −5 s / +5 s · **B** stop / back | Y or R · Enter · X · ← / →; N rename, Del delete |
| Voice Memos, recording | **A** or **B** stop and save · **X** pause / resume | Enter or Esc · Space |
| Paint | D-pad one pixel (hold to speed up) · left stick glide · **A** draw (held), or a shape's corner · **B** cancel the shape / back · **X** tools · **Y** colours · **L1 / R1** undo / redo · **L2 / R2** zoom · **Select** File (save, wallpaper, game cover) | arrows (Shift: 8 px) · Enter or Space · Esc · T · C · Z or Ctrl+Z / Ctrl+Y · - / + (0 fit) · M; [ ] brush size, 1–8 tools, G grid, Ctrl+S save. A mouse draws with the left button and zooms with the wheel |
| Paint, tools sheet | D-pad choose · **Left / Right** brush size · **A** use · **B** close | arrows · Enter · Esc |
| Stopwatch | **A** start / pause · **X** lap · **Y** reset (once paused) · **L1 / R1** Stopwatch ↔ Countdown | Enter · X · Y · Tab |
| Stopwatch, countdown | **Left / Right** hours, minutes, seconds · **Up / Down** change (hold repeats) · **Y** next preset while setting, reset once started · **A** start / pause | arrows · Y · Enter |
| Solitaire | D-pad focus (**Up / Down** in a column: how many cards to lift) · **A** pick up / put down · **B** put back / leave · **X** draw · **Y** smart move · **L1 / R1** undo / redo · **Start** game menu | arrows · Enter or Space · Esc · D · F or Tab · Z / R (Ctrl+Z / Ctrl+Y) · M; N new game. Click a pile to pick up or put down, double-click for a smart move |

A started stopwatch or countdown also shows in the [in-game menu](#in-game-menu-hold-select--l2)
and on its own **Quick Settings** button. **Create ZIP** is in Files: see
[Archives](#archives).

## Tools: search, pins and quick checks

On the Tools hub, **Y** searches as you type and **X** pins the focused tool to
a Pinned section at the top; **B** clears a search before it leaves. The five
quick tools are in [Tools](TOOLS.md); these are their keys.

| Screen | Pad | Keyboard |
|--------|-----|----------|
| Connection check | **A** a button or a step's details · **X** run again · **Y** copy the report · **B** back | Enter · X · Y · Esc |
| What's using space | **A** open a folder (a file: show it in Files) · **X** show in Files · **Y** largest files ↔ folders · **Left / Right** on the drive row: another drive · **B** up a folder, then back | Enter · X · Y · ← / → · Esc |
| Verify a file | **A** copy a checksum, choose, compare · **X** copy the SHA-256 · **Y** another file · **B** back (in the picker: up a folder) | Enter · X · Y · Esc |
| Screen test | **A**, **Right**, **Down** or **R1** next card · **Left**, **Up** or **L1** previous · **Y** auto (every 3 s) · **X** hide the caption · **B** leave | Enter, Space or → · ← · 1–9 a card · Y · X or H · Esc |
| QR maker | the keyboard, then **A** or **B** closes the code | Enter · Esc |

## Games

Open **Start → Games**. Snake, Tetris, 2048, Pong, Minesweeper and Flappy all
use the D-pad; **A** selects or performs the primary action and **B** leaves a
running game. **R** restarts a lost round.

| Game | Special controls |
|------|------------------|
| Minesweeper | **A** reveal · **X** / **Y** flag · arrows move |
| Flappy | **A** / **Space** flap · **R** restart |
| Solitaire | Klondike with undo and resume: see [Everyday apps](#everyday-apps) |

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

### Play time by game

The Playtime screen's third tab, **By game** (L1 / R1 or Tab to reach it), ranks
the games you started from JukaHub by how long you played them **Today**,
**This week**, **This month**, **This year** or **Ever**, each with a bar
against the longest. Press **X** to add the same time up by console instead.
Weeks start on Monday.

| Button | Action |
|--------|--------|
| Left / Right | Previous / next period |
| X | Games or consoles |
| Up / Down, Page Up / Page Down, Home / End (or wheel) | Scroll the list |
| L1 / R1, Tab | Switch tab |
| B | Back |

**Ever** is the lifetime figure, complete from the first day. The other periods
count from the day this version first ran: time before that was not kept by day
and game, and the list says from which date it counts. Days are kept for about
400 days in `playtime-days.json` in the data folder (a profile keeps its own),
and move to the right date when network time corrects a clock that started at
the wrong one. A game's **Game options** sheet shows its time this week and
ever.

### RetroAchievements

On a TrimUI Smart Pro, open **Start → RetroAchievements**,
**Emulators → RetroAchievements**, or open **Playtime & Achievements** and press
**Y**. The screen shows recent unlocks and completion progress for the account
already configured on the card.

JukaHub reads the username from Spruce or RetroArch and the Web API key from
`Saves/cheevos/apikey.txt` (or Spruce's saved `webApiKey`). It never edits those
files. If nothing is configured, use **X** → **A** to enter a username and a
32-character key from `retroachievements.org/settings → Keys`, or pick **From
your phone**: its code opens the Phone Companion's **RetroAchievements** card,
where you type the username and paste the key on the phone. The handheld checks
them with RetroAchievements before saving, and the code closes when they arrive.
The card is on the companion page all the time too.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Move through recent unlocks |
| A | Edit the selected account field, or show the phone code (setup screen) |
| X or Start | Account settings |
| Y | Refresh from RetroAchievements |
| B | Back / close |

The last successful sync is cached on the SD card, so the screen still shows
your history when Wi-Fi is unavailable. A key entered in JukaHub is stored only
in its data folder and is never displayed in full.

## Music player

Opening **Music** shows Albums, Artists, Audiobooks, History and Radio. Use **L1/R1** or
**Left/Right** to switch shelves, **Up/Down** to move, **A** to open or play,
**X** to rescan, and **B** to step back. From playback, **B** returns to the
shelves. Audiobooks are read from an `Audiobooks` folder and resume at the
saved chapter. On **Radio**, the first row opens the Radio directory (Top,
Country, Genre and Search tabs: **L1/R1** or **Left/Right** switch tabs, **A**
plays, **X** saves the station or takes it out, **Y** reloads or searches, **B**
steps back) and the rows under it are your saved stations — see
[MUSIC_PLAYER.md](MUSIC_PLAYER.md#radio). While something plays, a **Now
playing** row sits on top of the Radio shelf and of every directory tab:
**A** stops it (or tunes in again) and **X** opens the player; **A** on the
playing station's own row does the same. A station opens the player with the
pad on Play / Stop.

| Button | Action |
|--------|--------|
| Up / Down | Move through the playlist; **Up** past the first track steps onto the media controls (previous, play, next, stop, shuffle, repeat, favourite, volume), **Down** goes back to the list |
| A | Play the highlighted track, or press the media control the ring is on |
| X | Play / pause; on an internet radio station Play / stop (a station cannot pause) |
| Y | Favorite the playing track |
| Left / Right | Seek −10 s / +10 s; on the media controls, choose one (a radio station cannot be seeked) |
| L1 / R1 | Previous / next track |
| Start | The player's options: the Audio panel with **Now playing** on top — full-screen visualizer, lyrics (a `.lrc` file beside the song, or the lyrics stored inside it), sleep timer (off, 15, 30, 60 min), shuffle, repeat, speed, auto-exit and song search. Start or B closes it; in the full-screen visualizer Start puts the visualizer away |
| Guide (logo button, where the pad reports it) | Toggle shuffle |
| Select | Turn the screen off (playback continues) |
| X, then D-pad Right (within 0.45 s) | Open song search |
| Y, then D-pad Left (within 0.45 s) | Toggle background playback |
| L2 | Open / close the 31-band equaliser |
| R2 | Open / close Sound effects (Tape Mode, reverb, delay, chorus, flanger, speed) |
| B | Close the visualizer, then go back |

In the full-screen visualizer, **Left / Right** pick one of the ten visualizers
and **Y** turns the dithering on or off. The footer names these buttons on a
pad and the player's keys on a keyboard (Enter, Space, T sleep, L lyrics, K
options, V visualizer…). The **iPod** layout (Start → Now Playing layout) has
its own map: see
[MUSIC_PLAYER.md](MUSIC_PLAYER.md#ipod-layout). On a keyboard, **X** opens
Sound effects and **G** the equaliser in every layout.

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
| Y | Cycle presets (Flat, Bass, Vocal, Rock, Night, then the iPod's: Acoustic … Vocal Booster) |
| X | Flatten the selected band |
| G / B | Close the panel |

On a pad the panel opens with **L2**: the D-pad works as above, **Y** steps the
presets, **X** flattens the band and **B** (or L2) closes it.

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
| L1 / R1 | Move a screenful; L2 / R2 jump to the first / last row. In two panes L1 / R1 pick the left / right pane |
| A | Open a folder or a file; an archive opens its sheet ([Archives](#archives)) and a zipped game in a console folder starts (while Chat is picking files, attach the file, or the marked rows) |
| B / Back | Up one level, or back to Home at the root (while Chat is picking files, back to the chat) |
| Y | The action sheet |
| X | Mark the row under the cursor and step down |
| Mouse / touch | A chip in the breadcrumb bar to jump, the sort chip to change order, a row to open it |

The action sheet (**Y**) holds **New folder**, **Edit text**, **Open with…**,
**Extract…**, **Send to chat**, **Rename**, **Copy**, **Cut**, **Paste**, **Delete**, **Select all**,
**Sort**, **Connect to device** and **Remote files**. Marked rows are tinted and their type chip becomes a tick, so
a selection is visible in the list rather than only in the sheet. When nothing
is marked the actions apply to the row under the cursor, which is the rule a
desktop manager follows. **Delete** arms on the first press and acts on the
second, and a paste never overwrites: a second copy of the same name lands as
`name (2).ext`. A script (`.sh`) in Downloads, where files saved from the chat
land, runs on a second **A** within five seconds only. The clipboard holds paths, so **Copy** and **Cut** work between
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

### Two panes

**Y → Two panes** shows two folder lists side by side, each with its own
folder, cursor and marks; the setting is remembered. **L1** works in the left
pane and **R1** in the right one (**Tab** switches on a keyboard; a click on
the other pane switches to it with the cursor on the row clicked); the pane
being worked in has the accent edge and the full breadcrumb bar, the other one
shows its folder name and what it holds. Everything in the Y sheet acts on the
pane being worked in, and two rows join it after **Paste**:

| Row | What it does |
|-----|--------------|
| **Copy to other pane** | Copies the marked rows (or the one under the cursor) into the other pane's folder. A name that is taken gets " (2)", nothing is overwritten. |
| **Move to other pane** | The same, then the originals go. On one card this is a rename and instant; to another card or drive the copy finishes before the original is removed. |

The transfer runs in the background, one at a time: the other pane's bar reads
"Copying 2 of 5 here…" over a progress line, the sheet's row shows the item
being written (**A** on it twice stops after that item), and when it is done
both panes are read again with the cursor on the first new item. A system
folder (as the source of a move, or as the destination) asks twice, like the
other changes there. **F5** / **F6** on a keyboard open the sheet on Copy /
Move to other pane. One pane stays the default and is unchanged, and Files
opened to pick files for the chat or background music always shows one pane.

### Pictures: rotate, rename, delete

In the picture viewer, **X** (the **More** chip, **Tab** on a keyboard) opens
the picture's options:

| Row | What it does |
|-----|--------------|
| **Rotate right** | Turns the picture a quarter clockwise and saves it (**R** on a keyboard). PNG stays PNG; JPEG stays JPEG at quality 92, stood upright by its camera orientation first and saved without it, so it is never turned twice — the camera details in the file (EXIF) are not kept. WebP, GIF, BMP and other formats turn on screen only, and the viewer says so. Pictures larger than about 20 megapixels are not turned on the handheld. |
| **Rename** | A new name on the on-screen keyboard (**F2**), in the same folder. The extension is kept; an empty name, a `/` or a name already taken is refused. |
| **Delete** | Deletes the picture on the second press (**Del** opens the sheet on it) and shows the next one; after the last, the viewer closes. |
| **Set as lock screen** | The idle-screen photo (**L**); **Remove from lock screen** once it is. |
| **Mark up in Paint** | As **Start**: Paint opens on a copy. |

A turned picture is written to a hidden temporary file beside it and renamed
over it once complete, so a power cut leaves the old or the new picture, never
half of one. Files under the viewer is read again after every change, so its
list shows the new name, size and thumbnail; a renamed or deleted lock-screen
picture is followed or cleared.

### Archives

**A** on an archive — `.zip`, `.tar`, `.tar.gz` / `.tgz`, `.tar.xz`, `.tar.bz2`,
a lone `.gz`, `.xz` or `.bz2`, `.7z` or `.rar` — opens its sheet: the format
and size and, for a zip (or a 7z or RAR that 7-Zip can list), how many files it
holds, how big it unpacks and the first few things in it, then two rows. A
zipped game in a console folder (`Roms/GBA/game.zip`) starts instead; **Y →
Extract…** opens the sheet for it, and with several archives marked, **Y →
Extract** unpacks them all, one after another.

| Row | What it does |
|-----|--------------|
| **Extract** | One top-level item (a folder, a single game) lands next to the archive; several go into a new folder named after it. Nothing is overwritten: a name that is taken gets " (2)". |
| **Extract here** | Everything goes into the archive's own folder: folders are merged and a file of the same name is replaced (never the archive itself). When that would replace something (or, for a tar, could), the first press arms it and the second runs it. |
| **Cancel extraction** | While it runs (**Cancel** while it waits its turn). The folder is left exactly as it was; once the result is being moved into place it can no longer be stopped. |

**Y → Create ZIP** goes the other way: it bundles the marked files and folders
(or the one under the cursor) into a `.zip` in the same folder. Name it (it
starts as the item's or the folder's name; a taken name gets " (2)") and
press **Create**. The ZIP is made in the background — the row shows
"Zipping · 42%", and **A** on it twice stops it, leaving nothing behind — with
a free-space check first; links are never followed, dates are kept, and
pictures, music, video and archives are stored as they are while the rest is
compressed quickly. When it is done the cursor lands on the new ZIP.

The archive is unpacked into a hidden `.jukahub-extract-…` folder beside it and
moved into place at the end, so a cancel, a full card or a damaged archive
leaves nothing behind. After **Extract** the cursor lands on what came out when
it was still on the archive; otherwise it stays on its file. Progress shows in the sheet,
along the bottom of the Files bar once **B** hides the sheet (the bar's reading
replaces the folder's totals; the marked count stays), and in the activity
strip on other screens, where the chip's X cancels and a tap opens Files on the
archive. The extraction carries on behind any screen and says how it went in a
toast. The built-in readers (zip and the tar family) change names a FAT card or
Windows cannot store (`a:b.txt` becomes `a_b.txt`), skip a name that climbs
out with `..`, and leave links, devices and macOS metadata (`__MACOSX`,
`.DS_Store`) out. 32 MB of the card are kept free — a zip is checked before it
starts, a tar as it goes, a 7z or RAR from 7-Zip's listing, and 7-Zip is
stopped when the card gets down to it.

7z and RAR archives, and zips packed with something other than Deflate, are
unpacked by 7-Zip: `7zz` on the handheld (JukaCrossMix ships it in
`System/bin`), an installed 7-Zip on Windows — or, for a 7z, 7-Zip's own
`7zr.exe`, fetched once and checked against its published digest — and
Windows' `tar.exe` for RAR when no full 7-Zip is installed. 7-Zip lists the
archive first, and one it cannot list, or one that holds symbolic links (a FAT
card cannot hold them, and an older 7-Zip could follow one), is not unpacked;
the sheet says why. Password-protected archives cannot be opened yet; the sheet
says so (for a 7z or RAR once 7-Zip has looked at it).

### Open with

**Y → Open with…** lists every way to open the highlighted file: JukaHub's own
viewers that handle it (JukaHub's choice is tagged), **Open with Windows** on a
PC, the TrimUI apps on the card that open the type, **Get <App>** rows that
lead to the [App Center](#app-center), and **Another app…**. What each row
means is in [OPEN_WITH.md](OPEN_WITH.md).

| Pad | Keyboard | Action |
|-----|----------|--------|
| Up / Down | Up / Down | Choose a row |
| A | Enter | Open the file there, this once |
| X | X | Always use it for this extension (games in a console folder always play) |
| Y | Y | Forget the default: back to JukaHub's choice (*No default* where KOReader or an app takes the type) |
| L1 / R1 | PgUp / PgDn | Page the list |
| B | Esc | Cancel |

**Y → Default apps…** (shown once a default is saved; also Home search
"default apps") lists each extension and its app: **A** / Enter changes it,
**X** resets it, **B** / Esc closes. The text page's menu and the reader's
**Select** menu have **Open with…** too; a document the reader cannot show
offers it on **Y**.

### Text and hex editor

A on a text file — a script, a config, a source file, a log — opens the text
editor; a file that is not text opens the hex editor, and **Open with… → Hex
editor** (or the text editor's **View → Hex editor**) shows any file's bytes.
The full guide is [TEXT_EDITOR.md](TEXT_EDITOR.md).

| Pad | Keyboard | Text editor |
|-----|----------|-------------|
| Up / Down, L1 / R1 | Arrows, PgUp / PgDn | Move a line, a page |
| A | Enter | Edit the line on the keyboard |
| X | X / Insert | Insert a line after it |
| R2 | Delete | Delete the line |
| Y | Y / Ctrl+S | Save (a `.json` file must be valid JSON) |
| Start | Tab | View: wrap, line numbers, text size, syntax colours, Highlight as, Hex editor, Open with… |
| – | H | Syntax colours on or off |
| B | Esc | Close (asks first when there are unsaved changes) |

Shell, Perl, Python, JavaScript/TypeScript, JSON, C/C++, Java and config files
(INI, `.cfg`, YAML, TOML) are coloured by keyword, string and comment, the
language found from the file's name or its `#!` line; **View → Highlight as**
picks another one for the open file.

| Pad | Keyboard | Hex editor |
|-----|----------|------------|
| D-Pad | Arrows | Move a byte (Left / Right) or a row (Up / Down) |
| L1 / R1, L2 / R2 | PgUp / PgDn, Home / End | Page; first / last byte |
| A | Enter | Edit the byte: Up / Down count its digit, Left / Right step on, A keeps it, B puts it back |
| – | 0–9, A–F | Type hex digits straight in |
| X | X | Type bytes from the cursor on: `4A 75 6B 61`, or text in quotes |
| Y | Y / Ctrl+S | Save (nothing is written before) |
| Start | Tab, Ctrl+G, Ctrl+Z | View: go to offset, undo, show as text |
| B | Esc | Close (asks first when bytes changed) |

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

## Bluetooth devices

Open it from the **Bluetooth button** in the top bar, **Settings → Network →
Bluetooth devices**, **Quick Settings → Bluetooth Devices**, **Start →
Bluetooth**, search ("connect headphones" finds it too), or the **Controllers**
row in PS Remote Play. It needs `bluetoothctl`; without it — on Windows, macOS
or a Linux desktop with GNOME's or Blueman's settings — the page offers **Open
Bluetooth settings**, the system's own pairing screen, instead.

| Input | Action |
|-------|--------|
| D-pad / arrows | Move through the rows |
| A / Enter | Switch Bluetooth on or off, connect or disconnect a paired device, pair a device under Nearby, search, or show how to put a pad in pairing mode |
| Y / `y` | A paired device's sheet: what it is, whether it reconnects by itself, a pad's battery and order, a headset's codec, Identify (a connected pad buzzes and blinks), Connect or Disconnect, Forget |
| X / `x` | Search (ten seconds) |
| L1 / R1, PgUp / PgDn | Page up and down; Home / End jump to the ends |
| B / Esc | Close the sheet, then leave the page |

The page opens with the ring on the first paired device (a connected one
first) and searches by itself only when nothing is paired; a pad woken with its
PS button, or a headset switched off, shows on its row within about two seconds.
Until the list has loaded, **A** on the switch cannot turn a powered radio off.
When the firmware left Bluetooth stopped, switching it on — or Connect, Pair
and Search — starts it first, and the switch or the device's row says what it
is doing meanwhile. Pairing a controller, what its row says and
what to do when a pad shows **No input** are in
[REMOTE_PLAY.md](REMOTE_PLAY.md#bluetooth-controllers).

A connected pad's row adds its place in the order the pads connected and its
battery (**Pad 1 · 80% battery**), read from the kernel; a pad that drops to
15% says so once, and again only after it was charged. A connected headset's
row names its codec (SBC, AAC, aptX) once BlueALSA reports it, and its sheet
says what that codec means for delay.

Remembered devices come back by themselves: shortly after JukaHub starts, after
every wake from sleep and when Bluetooth is switched on, paired and trusted
headphones and pads that are not connected are called — three tries at most
(at once, 20 s and a minute later), up to four devices a try, then it gives up
quietly until the next start, wake or switch. It never switches Bluetooth on,
never runs while a game is on screen or this page is open, and leaves alone a
device you disconnected here until it connects again some other way.

When a headset connects, every JukaHub sound moves to it (a game started
afterwards gets it too); when it goes, sound returns to the speaker and a
playing video or song pauses, like a phone. The speaker and each headset keep
their own volume (`bt-output-volume.json` in the data folder).

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

**Report a bug** sends your report with redacted diagnostics straight from the
device (**Preview**, Y, shows all of it) and shows the public GitHub issue it
became; without a connection it waits on the device and goes out by itself, and
only a device that cannot send it falls back to a code for your phone
([BUG_REPORTS.md](BUG_REPORTS.md)). **Anonymous diagnostics** is off by default;
when on, every ten launches it files a public informational GitHub issue with
build and coarse device facts — never designs, filenames, logs, links or account
details.

## QR codes

Three codes exist to be scanned by another device: the **Phone Companion**
pairing code, the **Screen Share** link, and **Community & support →
Diagnostics export**. In their cards they are a few hundred pixels square over
a dimmed screen, which is the shape a phone camera reads worst, so each one can
also be enlarged on its own.

The **Phone Companion** card also shows a browser address: the handheld's IP
address, plus a USB-network address when one is active. Type either as shown
on the card, including any port, then enter its
six-digit PIN.
The code already contains the PIN.

| Input | Action |
|-------|--------|
| A (on the selected code), or a tap on it | Fill the screen with the code and raise the backlight to full |
| Up / Down, or Left from Share or Games too (Screen Share) | Select the code; the card opens on **Share** |
| A / B / Esc, or any tap | Close the enlarged code and put the backlight back where it was |

The **QR maker** (Tools → Everyday apps) shows its codes on the same card. There **X** (**S** on a keyboard) saves the code as a PNG in the screenshots folder and leaves it on screen; pairing codes are never saved.

On **Diagnostics export**, **B** still leaves the page and the enlarged code is
the page's only control, so **A** enlarges it instead of going back.

## Screen Share

**Start → Tools → Screen Share** (search finds it as *screen share*, *cast*,
*tv* or *hdmi*) shows the link that puts the handheld's screen, games included,
in a browser on a TV, laptop, tablet or phone on the same Wi-Fi. The card opens
on **Share**; the README's
[Screen Share](../README.md#share-the-screen-on-a-tv-screen-share) section
describes what viewers get.

| Input | Action |
|-------|--------|
| Up / Down | Move between the code, **Share**, **Quality**, **Games too** (handheld only), **Sound** and **Controllers**, wrapping |
| Left / Right | On **Quality**: the previous / next preset. On the other rows, Left selects the code and Right comes back |
| A / Enter | Code: enlarge it · Share / Games too / Sound / Controllers: switch · Quality: next preset · Controllers while a viewer asks to play: let them play |
| X | No to the viewer who asks to play · take control back from the viewer who plays |
| Y | New link: the old one stops working at once, and a viewer who plays stops; the Phone Companion's PIN is not changed |
| B / Esc | Close |
| Click / tap | Runs the row (on **Quality** the left half steps down, the right half up); a click outside the card closes it |

A viewer's request to play turns the **Controllers** row into the request (the
viewer's browser and address); with the panel closed, a toast and a buzz say a
viewer asks, and opening the panel lands on it.

On the viewer page, **F**, the full-screen button or a double-click (double-tap)
toggles full screen, and **M** cycles Fit, Fill and Pixel-perfect; the controls
hide after three seconds without the pointer moving. **Sound** plays the
handheld's sound there, and **Play** asks to play it. Add `&tv=1` to the link
for a TV browser: no controls, just the picture, and the remote's first key
turns the sound on.

While a viewer plays, the page's keys are the handheld's buttons (F and M then
belong to the game; use the buttons):

| Viewer's key | Handheld |
|-------|--------|
| Arrows | D-pad |
| X / Z / S / A | A / B / X / Y |
| Q / W | L1 / R1 |
| E / R | L2 / R2 |
| Enter | Start |
| Shift or Backspace | Select |
| H | Menu |

A controller on the viewer's side works by position, like the handheld's own
pad (its right face button is A, the bottom one B, the top one X, the left one
Y); **Buttons by letter** on the page's bottom bar switches to the letters
printed on an Xbox-style pad. Sticks, analog triggers, L3/R3 and the Home
button (Menu) work too. On a phone or tablet the page shows a touch pad over the
picture (**Hide touch pad** puts it away; a controller found hides it). The
in-game menu and the game assistant, which take the handheld's pad for
themselves, answer only the handheld's own buttons.

## Voice orb

Press **Menu** — the TrimUI button on a Smart Pro or a Brick — or **F4** on a
keyboard, on any screen, and a glowing orb rises in the bottom-right corner and
says **Listening…**. Say a command ("open YouTube", "volume 50", "take a
screenshot") or ask a question. It stops listening about a second after you
stop talking; hold the button instead and it listens until you let go. It runs
the same commands as the Voice command page below, and a question that names
nothing on the handheld goes to Juka, whose answer appears next to the orb.
See [VOICE.md](VOICE.md#the-voice-orb).

| Button | Action |
|--------|--------|
| Menu (F4) | A tap opens the orb and listens (it acts when Menu comes up); while it listens, stop and send; on an answer, ask again. Held about half a second: talk until you let go |
| A (Enter) | While it listens, stop and send; during "Opening…", open now; in a list, open the highlighted match; on an answer, its fix ("Add Groq key", "Use my phone", "Search for it") |
| B (Esc) | Cancel: stop listening, drop the command, put the orb away |
| D-pad (↑ ↓) | Choose when it lists matches |
| Any other button | Puts a finished answer away and does its own job |

A press of Menu that another button joins is a chord, not a voice press: the
**Menu + volume** does not open the orb, and **Menu + Start** still quits like
**Select + Start**. With the on-screen keyboard up,
**Menu** (or **F4**) is voice typing into the field instead, and on the Voice
command page it is that page's own microphone. Over a modal panel, the
first-run tour, the dock, the arcade, JukaLand, a browser game or a running
game, **Menu** keeps its old job (Back). **Settings → Controls → Menu button:
voice** turns the Menu binding off for good; **F4** stays the voice key on a
keyboard.

Juka's answers are spoken while **Settings → Voice & AI → Juka's voice** is on
(the default); any button stops the voice. **R2** is voice typing on the
on-screen keyboard whatever the Menu setting, and in a game **Select + R2**
opens the orb over the paused game when the Game assistant is on
([above](#ask-juka-in-a-game-hold-select--r2)).

## Voice command

Open it from **Start → Tools → Voice command** or with **Y** in the Home search
sheet (**Menu** and **F4** open the voice orb, the quick way to the same
commands). Say what to open — a screen, an app, a game, a setting — or what to
do: the volume, the brightness, a screenshot, Wi-Fi, Bluetooth, the music. See
[VOICE.md](VOICE.md).

| Button | Action |
|--------|--------|
| A | Listen; while listening, stop early; during the short "Opening…" pause, open now |
| B | Cancel while listening or during the pause; otherwise go back |
| X | Type a command instead |
| Y | Show the QR code for the Phone Companion's **Voice** page, where your phone's keyboard microphone does the listening |
| D-pad, then A | Pick one when the page lists matches |

On a keyboard, **Enter** listens (**F4**, and **Menu** on the pad, also start
and stop listening on the page), **X** types, **Y** shows the QR code and **Esc** goes back. A command
that opens a screen leaves the voice page behind, so **B** there returns to
where you were.

The page's **Shortcuts** button opens [Voice shortcuts](#voice-shortcuts).

## Voice shortcuts

**Settings → Voice & AI → Voice shortcuts**, the Voice command page's
**Shortcuts** button, or "open voice shortcuts": the words you taught the voice
assistant. See [VOICE.md](VOICE.md#your-own-words).

| Button | Action |
|--------|--------|
| D-pad | Choose a word |
| A (Enter) | Change the word; on **Teach a word**, start |
| X (X / Delete) | Delete the word (it asks first) |
| Y | Try it |
| B (Esc) | Back |

While teaching: **A** says the word (or goes on to the next step), **X** types
it instead and **B** goes back a step; when picking what it opens, **L1 / R1**
switch the group (All, Emulators, Screens, Apps, Games, Settings, Media,
Files), **X** filters and **A** picks. Every button, row and the microphone
disc also answer a mouse or a tap, and the wheel scrolls.

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
them. The photo is set from the viewer: **X** opens its options, where **Set as lock
screen** (or `l` on a keyboard) makes it the backdrop, and the same row reads
**Remove from lock screen** once that picture is the backdrop.

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

## Synth

A pocket groovebox (Start menu → Synth, or Tools → Synth): seven lanes of a
16-step sequencer — kick, snare, hat, clap, bass, lead and chords — eight
patterns, and every sound synthesized on the device. Start plays and stops
on every page; L1 / R1 switch between the four pages.

| Page | Buttons |
|------|---------|
| Pattern | D-pad moves · A sets / clears a step · Y / X move a melodic step's note up / down the scale (on a drum lane: accent / ratchet) · A on a lane's name mutes it · L2 / R2 previous / next pattern |
| Play | The D-pad and Y X A are seven pads in the song's key and scale (the drums: kick, snare, hat, clap and three accented hits) · L2 / R2 change the instrument. With Mix → Record from Play on, what you play while the pattern runs is written into it |
| Sound | L2 / R2 instrument · Up / Down choose a setting · Left / Right adjust · X / Y big steps · A listens (on Surprise me: a random sound) |
| Mix | Tempo (A taps it), swing, key, scale, steps, pattern chaining, echo, volume, Record from Play; actions: copy, clear (press twice), random beat, the demo (press twice), Export to WAV |

Keyboard: Space plays, Tab / Shift+Tab change the page, `[` `]` are L2 / R2,
Enter, Y (or `=`) and X (or `-`) are A, Y and X, and 1–7 play the pads.
Exports land in Downloads/music as `Synth 001.wav`… where the Music player
lists them. The project saves itself to `synth.json` beside JukaHub (and goes
into backups). The Synth only sounds while its page is on screen.

## Pads

A 64-pad sample sequencer (Start menu → Pads, Apps → Pads or Tools → Pads):
four banks of sixteen pads, eight patterns of 16 to 128 steps, a song chain and
effects on the sticks. Start plays and stops on every page; L1 / R1 switch
between the six pages. See [Pads](PADS.md) for sounds, chops and exports.

| Page | Buttons |
|------|---------|
| Play | D-pad moves (Up / Down past the edge change bank) · A plays the pad · Y picks its sound · X edits it · L2 / R2 bank · Select: finger drumming, where the D-pad plays one row (Left, Up, Down, Right) and Y X B A the row under it, L2 / R2 move two rows and Select stops |
| Beat | D-pad moves · A sets / clears a step (hold A and move to paint the row) · Y / X note up / down a semitone · Select accent · L2 / R2 pattern |
| Pad | L2 / R2 previous / next pad · Up / Down choose a setting · Left / Right adjust · X / Y big steps (Pitch: an octave) · A runs the row or plays the pad · Select plays the pad |
| Song | Settings on the left, the song's slots on the right: on a slot Left / Right change its pattern, Y repeats it, X removes it, A opens its pattern |
| FX | Left / Right choose a stick direction's effect · on a Hold row A turns it on / off and Left / Right set the amount |
| Mix | Tempo (A taps it), swing, master, compressor, echo, reverb, Record live hits; actions: Export to WAV, starter kit, clear bank, new project (the last three press twice) |

Sticks: on Play and FX both sticks play effects (how far they lean is how
much); on the other pages the right stick does and the left one moves the
focus. In the sound browser A puts the sound on the pad, Y listens and B goes
up a folder. Keyboard: Space plays, Tab / Shift+Tab change the page, `[` `]`
are L2 / R2, M or `` ` `` is Select, and on the Play page `1 2 3 4`, `Q W E R`,
`A S D F` and `Z X C V` play the bank's four rows.

## Alarm

On the list:

| Button | Action |
|--------|--------|
| Up / Down | Move through the alarms |
| A | Edit the focused alarm |
| X | Toggle the focused alarm |
| Y | New alarm |
| L1 (keyboard **Del**) | Delete the focused alarm (press twice) |
| B | Back |

In the editor (rows: Hour, Minute, Label, Ringtone, then Save and Delete):

| Button | Action |
|--------|--------|
| Up / Down | Move between the rows |
| Left / Right | Change the focused value (hold to repeat) |
| L1 / R1 | Time minus / plus 10 minutes |
| A | Next row; on Label, type it on the on-screen keyboard; on a button, press it |
| X | Ringtone: pick a track from your music · Label: clear it |
| Y | Save from any row |
| B | Back to the list (with unsaved changes, press twice) |

On a keyboard the number keys type the hour or minute, Enter on Label opens the
keyboard (or just start typing), PgUp / PgDn step 10 minutes and Y or S saves.

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

| Pad | Keyboard | Action |
|-----|----------|--------|
| D-pad | Arrows | Move over the keypad; Right from the right-hand column walks into the history |
| A | Space (Enter when nothing was typed) | Press the focused key; in the history, use that answer |
| — | Digits, operators, `( )`, names (`sqrt`, `sin`, `pi`, `ans`) | Type the sum |
| — | Enter, `=` | Answer (brackets left open are closed) |
| X | `x` | Type the graph's x; in the history, put that sum back to edit |
| Y | Y | Graph / history |
| L1 | Del | Clear the line |
| R1 | Backspace | Delete the last character or a whole `sin(` |
| — | Ctrl+C / Ctrl+V | Copy the answer / paste a sum |
| B | Esc | Back (from the history: back to the keys) |

**2nd** switches sin, cos and tan to their inverses, ln to eˣ, log to 10ˣ, x²
to x³, √ to ³√ and xʸ to ʸ√x for the next key; **DEG / RAD** sets the angle
unit (degrees by default, also a tap on the display's corner).

## Terminal

The built-in shell, useful for diagnostics. Most commands need no typing:
see [TERMINAL.md](TERMINAL.md) for the Command Deck, Favorites, macros and the
keyboard's token chips.

| Pad | Keyboard | Action |
|-----|----------|--------|
| A | Typing | Open the on-screen keyboard / type a command |
| Start | Enter | Run the line (saved to the history file, so it survives a restart) |
| Select | Ctrl+P | Command Deck: ready-made commands by category, filled in with pickers |
| Y | Ctrl+R / F7 | Command history (in it: A insert, Start run, Y pin to Favorites, X delete; keyboard Enter, Ctrl+Enter, Ctrl+S, Del) |
| X | Ctrl+U / Ctrl+C | Clear the line / stop a running command |
| — | Tab | Complete the command or path; on an empty line, open the on-screen keyboard |
| D-pad Up / Down | Ctrl+Up / Ctrl+Down | Scroll the output (Up / Down alone recall history on a keyboard) |
| L1 / R1 | PgUp / PgDn | Page the output |
| — | Ctrl+L | Clear the output |
| B | Esc | Back |
| — | `help` | Built-in command list |
| Start | Enter | While a program reads input (a Java program asking a question): send the typed line to it; an empty line is an answer too |
| — | Ctrl+D | End the running program's input |

With the on-screen keyboard open, the strip above the keys offers command
tokens (flags, paths, history); **Select** takes the first one.

**Command Deck** (Select / Ctrl+P, or *Commands* in Tools, the Start menu and
Home search):

| Pad | Keyboard | In the list | In a form |
|-----|----------|-------------|-----------|
| Up / Down | Up / Down | Choose | Choose a blank |
| Left / Right, L1 / R1 | Left / Right, Tab, PgUp / PgDn | Category | Change the value (L1/R1, PgUp/PgDn: ±10) |
| A | Enter | Open or run | Pick a value |
| Start | Ctrl+Enter | Run now | Run |
| Y | Ctrl+E | Edit in the prompt | Edit in the prompt |
| X | Ctrl+S | Save to Favorites (in Favorites: remove; also Delete) | Save to Favorites |
| B / Select | Esc / Ctrl+P | Close | Back |

In a folder picker the first row (or Y / Ctrl+Enter) uses the folder on
screen and Left goes up. Deleting a file and stopping a process ask first.

### Interactive programs

`shell` (and bare `sh`, `bash`, `cmd` …), full-screen programs such as `htop`,
`vi`, `nano`, `less` or `mc`, REPLs typed with no arguments and `ssh user@host`
run in a session on a real terminal ([TERMINAL.md](TERMINAL.md#interactive-programs)).

| Pad | Keyboard | Action |
|-----|----------|--------|
| D-pad | Arrows | Arrow keys |
| A | Enter | Enter |
| B | Esc | Esc |
| Hold B (0.8 s) | Ctrl+Shift+Q | Leave; the session keeps running (Start, or Ctrl+Shift+Q, on the line terminal resumes it) |
| X | Backspace | Backspace |
| Y | — | On-screen keyboard; each key goes straight to the program |
| L2 | Tab | Tab |
| R2 | Ctrl | Ctrl for the next key |
| L1 / R1 | PgUp / PgDn | Page in a full-screen program; scroll back on the shell's screen |
| — | Shift+PgUp / PgDn, wheel | Scroll back |
| Select | — | Special keys: Esc, Tab, Ctrl+C/D/Z/X/O/L/R, Home, End, PgUp, PgDn, F1–F12, `q`, `:wq` |
| Start | Ctrl+Shift+M, or click the title bar | Session menu: Leave, End session, Paste, text size, Keys help |
| — | Ctrl+Shift+V | Paste |
| — | Ctrl+Shift+Plus / Minus | Text size |

With a keyboard every other key (F1–F12, Ctrl+Q, Ctrl+L, F4 …) goes to the
program; the Menu button (voice) and Select + Start (quit) stay global. A
password a program asks for (sudo, su, ssh) shows as dots on the on-screen
keyboard, and the grid moves up so the line being typed stays visible.

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
| F4 | The voice orb: talk to JukaHub (hold it to talk; on the Voice command page F4 starts or stops listening) |
| F8 | Watch Party panel |
| F11 / Alt+Enter | Toggle fullscreen |
| F12 | Screenshot |
| PgUp / PgDn | Page up / down (previous / next results page in Media) |
| Shift+Left / Shift+Right | Skip −10 / +10 s in the video player |
| J / K / L, M | Skip −10 s / play or pause / skip +10 s, and mute, in the video player |
| N / P | Next / previous video in the player's list (also Shift+Down / Shift+Up) |

Mouse and touch work as well: clicking or tapping activates a card or tile, and
hovering highlights the card under the pointer. On a touch screen a sideways
swipe changes the Tabs Home's tab and an up or down swipe scrolls it; the
picture viewer and the Media screen take swipes too, and pinching zooms a
picture. A swipe anywhere else does nothing. A touchpad only moves the pointer.

---

## Related

- [Configuration reference](CONFIGURATION.md) — how a button in the config runs a trigger
- [Dock & ambient](AMBIENT.md) — the idle clock, slideshow, sleep timer and screen-off audio
- [Performance & compatibility](COMPATIBILITY.md) — per-game reports, boost plans and core builds
- [Building](BUILDING.md) — running a desktop build to use the keyboard bindings
