# Controls

JukaHub is controller-first: every screen is reachable with the D-pad and the
four face buttons. Keyboard and mouse are supported on desktop builds.

The top bar always shows the system status — a brightness button, a volume
button, a Wi-Fi meter, the weather chip, the clock, a battery gauge, the
now-playing title and the notification bell on the right, the **Apps** pill and
a **back** pill on the left. The bottom of every screen is the hint bar for real
controller bindings; there is no second clock or battery indicator down there.

The **Wi-Fi meter** sits immediately right of the volume button and is a button
on every screen: four bars that are hollow when there is no link and fill with
signal strength, plus `! Offline` while the device has no network at all. A
click or tap opens the Wi-Fi picker (scan, connect, enter a password). From a
controller it is part of the same focus row as the clock and the forecast: on
Home **Up** from the Settings card reaches the header, then **Left**/**Right**
walk the brightness and volume buttons, the meter, the forecast, the clock, the now-playing
chip, the update offer and the bell; in any other scene **Up** leaves the content for the top bar and
**Left**/**Right** walk it. **A** on the meter opens the picker, exactly like a
tap. The same picker is also
**Start → Wi-Fi Networks** and the Wi-Fi row in Quick Settings.

The **volume button**, a speaker, leads the cluster, immediately left of the
Wi-Fi meter, wherever JukaHub can drive a mixer: the codec on a TrimUI,
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
settings, so its brightness keys carry on from it, and the card follows those
keys. On a Linux PC it is the backlight; on a Windows laptop or handheld the
panel brightness (the level Windows' own slider shows), and on a Windows desktop
the monitor's brightness over DDC/CI. Where there is no such control the card
dims JukaHub's picture instead and says so in its last line. Quick Settings'
brightness row and **Settings → Display & theme → Brightness** show the same
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
the forecast open their cards, the Wi-Fi meter opens the picker, the trophy
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
clicked.
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
| L2 / R2 on Home (sections) | Q / E |
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
| Home | L2 / R2 sections · Y search | Q / E · **/** (F2 anywhere) |
| Home search sheet | X edit the search · Y voice command | type to edit |
| Home (Carousel, Widgets) | X edit | X |
| Home edit mode | X size · Y remove · L1 / R1 layout · B done | X · Del or Backspace · Shift+Tab / Tab · Esc |
| Start menu | L1 / R1 · L2 / R2 · X power | PgUp / PgDn · Home / End · X |
| Notification centre | L1 / R1 · L2 / R2 | PgUp / PgDn · Home / End |
| Media | L1 / R1 tabs · R2 more · X search · Y details | Tab or [ / ] · PgDn · X or **/** · Y or I; U undo, Del remove |
| Favorites | Left / Right tabs · X remove · Y undo | Left / Right · X, D or Del · Y or U |
| Live TV | X search · Y favourite | X · Y or F |
| Browser | Select menu · L1 / R1 scroll · L2 / R2 zoom · X links · Y search · L3 mode | M or the Menu key · PgUp / PgDn · - / + · X or L · Y or **/** · Tab; F find, N / P next / previous match, I site info, Backspace or Alt+← / Alt+→ back / forward, F5 reload |
| Video player | X captions · Y settings · L1 / R1 seek · L2 / R2 previous / next | X or C · Y or S · Shift+← / → · P / N (or Shift+↑ / ↓); F full screen, Q end playback, 1–4 speed |
| Chat | L1 / R1 tabs · Start send · X attach files · Y tab action | Tab · Ctrl+Enter · X · Y; Enter types inline, 1–3 pick a tab, R renames you, Del drops a waiting file |
| On-screen keyboard | A type · B close · X delete · Y submit · L1 / R1 shift / layer | type on the keyboard · Esc · Backspace · Enter · Shift; Ctrl+C / Ctrl+V |
| Settings | L1 / R1 sections · D-pad in an option sheet | Tab / Shift+Tab or PgUp / PgDn · arrows (Left / Right page the sheet) |
| Tools hub, Network tools | L1 / R1 section · X copy in a result sheet | PgUp / PgDn · X or C; R runs a tool, E edits its target |
| Voice command | A or Menu speak / stop · X type · Y phone QR · B back (cancels while listening or during the pause) | Enter · X · Y · Esc; F4 speak / stop |
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
| Game profiles | X · Y | X · Y or R |
| Windows apps | X setup · Y display · Start rescan | X · Y or D · R or F5 |
| APK Store | A options · X details · Y rescan · L1 / R1 tabs | Enter · X or Del · Y · Tab / Shift+Tab or PgUp / PgDn |
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
| Menu | The voice orb: press it and speak, or hold it while you talk (with **Menu button: voice** off in Settings → Controls, Back like B) |
| Start | The Start menu: every scene, game and tool |
| Select + Start (hold both) | Quit JukaHub |
| Select + L1 (hold) | Mouse mode: the D-pad moves a pointer and A clicks (see Virtual cursor) |
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
| Right stick | Move the virtual cursor (below); L2 / R2 scroll under it |
| Select + L1 (hold) | Mouse mode on / off: the pointer stays up and the D-pad and sticks move it (below) |

### Virtual cursor

The **right stick** is a mouse pointer (handy on the TrimUI Smart Pro, which
has no touch screen): move it and the pointer appears and follows it — a light
tilt is slow, full tilt crosses the screen in about a second and a half. **A**
clicks (hold A and move to drag a slider or a shelf), and **L2 / R2** scroll up
and down under the pointer (held, they keep scrolling). B and the other buttons
keep their usual jobs; the **left stick** stays the D-pad's twin. Any D-pad
press or left-stick step puts the pointer away (so A presses the focused item
again), and so do 30 seconds without touching the right stick; the right stick
brings it back where it was. A stick that rests a little off centre never
brings it up.

Screens that use the right stick themselves keep it: Chat scrolls its history
with it, JukaLand and browser games aim with it, and the stick calibration and
the controller test measure it. The pointer also stays away in Quick Settings
and the wallpaper picker. Turn it off in **Settings → Controls → Virtual
cursor**.

#### Mouse mode (hold Select + L1)

Hold **Select + L1** for about a second (0.7 s) to turn **mouse mode** on, and
again to turn it off; **F6** does the same on a keyboard. A short message says
which. In mouse mode the pointer stays up — no 30-second timeout — and the
**D-pad** moves it as well as the sticks: slowly at first, for a precise nudge,
then faster the longer you hold it, up to the sticks' full speed. The **left
stick** moves it too instead of the focus. **A** clicks (hold A and move to
drag), **L2 / R2** scroll under the pointer, and **B** and every other button
keep their usual jobs, so you are never stuck behind the pointer: B still goes
back. This is how the **TrimUI Brick**, which has no sticks, gets a pointer.

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
ports through PortMaster, a WPE browser. Most emulated games have no use for a
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

### Stick calibration

**Settings → Controls → Calibrate sticks** measures both sticks in about ten
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

This tunes JukaHub's own stick handling: stick navigation, the virtual cursor,
and what counts as the stick moving for the screensaver. Games and emulators
read the sticks themselves: use the TrimUI firmware's calibration app for the
hardware range every program sees, and PPSSPP's own calibration (Start menu →
Tools → Stick Calibration) for PPSSPP. The Controller test (Power tools) draws
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
| Quick Settings | Five rows, or ±5 on a slider | First / last row |
| Start menu | One window of the open group | First / last row of the open group |
| Home | — | Previous / next section |

### Liquid background

With **Settings → Display & theme → Liquid background** on (*Gentle* or
*Lively*), the backdrop is a shallow pool and the controls you already use move
it. It only watches: nothing is taken from the screen in front of it.

| Input | The water |
|-------|-----------|
| Moving the focus (D-pad, left stick, arrow keys) | A ripple under the tile it lands on |
| Left stick, while tilted (Smart Pro) | Stirs the water round the focused tile |
| Right stick (Smart Pro) | Drags a wake on its own, or follows the virtual cursor while it is up |
| Pointer (Brick, mouse mode) or mouse (PC) | Drags a wake |
| Tilting the device | Sloshes it — only where an accelerometer answered (**Tilt to slosh**) |

Reduce Motion keeps the ripples you make and drops the rain and the slosh.

### Wallpaper picker

**Settings → Display & theme → Wallpaper** or **Start → Device → Wallpapers**
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
| Screens | Home, then Trophies (with how many you have earned, e.g. "12/401"), then every other screen in `jukaconfig.json` — the quickest way to switch screen |
| Games | Emulators, Games Library, Game Profiles, PS Remote Play, RetroAchievements, Save Manager, Windows Apps, PortMaster Ports, APK Store, Smart Pro Compatibility |
| Tools | Downloads, Curation Hub, Audio settings, Smart Home, Screen Share, Voice command, Accessibility, Stick Calibration (when the install has one) |
| Device | Quick Settings, Wi-Fi Networks, Screenshot, Sleep Screen, Ambient, Wallpapers, Edit Home, About, Quit JukaHub |

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
| D-pad | Move between tiles, the header chips (brightness button, volume button, Wi-Fi meter, weather, clock, now-playing, update offer, bell, PS4/PS5, pairing QR) and the footer row |
| Left (from the weather chip) | Move onto the Wi-Fi meter; **A** opens the Wi-Fi picker |
| Left (from the Wi-Fi meter) | Move onto the volume button; **A** opens the volume card |
| Left (from the volume button) | Move onto the brightness button; **A** opens the brightness card |
| Right (from the clock) | Move onto the now-playing chip while something plays, then the update offer while a release is advertised, then the notification bell; **A** opens that player, installs the release, or opens the notification centre |
| Left (from the bell) | Move onto the update offer while a release is advertised; **A** installs it |
| Right (from the PS5 pill), Down (from the bell) | Move onto the pairing QR while it is shown; **A** (or a click or tap) shows it full screen at full brightness with the PIN and address under it, and **A** or **B** closes it |
| L2 / R2 | Previous / next section of the grid |

### Home layouts

**Settings → Display & theme → Home layout** picks one of four layouts:
**Standard** (your config's dashboard, the layout described above), the **Hero
grid** (the same cards as equal tiles), the **Carousel** and **Widgets**.
Standard and the Hero grid are fixed. The Carousel and Widgets show your own
list of tiles, which you arrange on the device; how it is stored is in
[CONFIGURATION.md](CONFIGURATION.md#home-tiles-hometiles).

The **Carousel** is one row of big cards: the focused card in the middle, its
neighbours smaller on either side, wrapping around at the ends. The focused
card's title and description sit under it, over a dot for each card (with
more than 15 cards, a count such as "7 / 24").

**Widgets** is a grid of tiles six columns wide, each as big as you make it. A
tile is a Home card, any Start-menu entry (a screen, a tool, a device action,
a plugin's button) or an info widget. Three rows fit on the screen and the
grid scrolls past them.

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
| X | X | Edit Home | Edit Home |
| Y | **/** (F2 anywhere) | Search | Search |
| Mouse | — | Click a side card to bring it to the middle, the middle card to open it; the wheel turns one card | Click a tile to open it; the wheel scrolls a row, and a focused tile it scrolls away hands the focus to one still shown (pointing alone never scrolls) |

#### Edit Home

There are five ways in:

- **X** (the **X** key) on Home while the Carousel or Widgets is showing;
- the **Edit** pill beside the power pill in Home's footer;
- a right-click on a tile, which starts editing on that tile;
- **Start → Device → Edit Home**;
- **Settings → Display & theme → Edit Home**.

The last two work from any screen: they open Home first, and on Standard or
the Hero grid they switch to Widgets (a message says so).

While editing, the greeting and the search pill give way to a strip:
**Edit Home**, a chip for each layout (the one in force tinted), **Reset** and
**Done**; the PS4 / PS5 pills and the pairing QR step aside. Every tile wears
a remove badge (a minus) on its top-right corner, and on Widgets the focused
tile shows its size ("2×1") on a tab in the middle of its bottom edge. An
**Add** tile with a plus ends the list (Home holds up to 64 tiles).

| Pad | Keyboard | In edit mode |
|-----|----------|--------------|
| D-pad | Arrow keys | Move between the tiles and up to the strip. Holding a tile, move it: Left / Right one place earlier / later, Up / Down to the place of the tile above / below (Down past the end makes it the last) |
| A | Enter | Pick the tile up, or put it down. On **Add**: the list of tiles to add. On the strip: that chip |
| X | X | The next size (Widgets only) |
| Y | Delete or Backspace | Remove the tile (**Add** stays) |
| L1 / R1 | Shift+Tab / Tab | The previous / next layout: Standard, Hero grid, Carousel, Widgets |
| B | Esc | Holding a tile: put it down where it is. Otherwise: Done |
| Start | F10 | The Start menu, which ends editing |
| Select | — | Nothing on its own, so the Select + L1 (virtual mouse) and Select + R1 (control profile) chords still work |
| L2 / R2 | Q / E | Nothing: Home does not page away mid-edit |

Leaving Home ends editing too. Every finished change (a drop, a resize, a
removal, an addition, a reset) is saved straight away, and again when editing
ends. The footer reads **D-Pad Navigate · A Move · X Size · Y / Del Remove ·
L1/R1 / Tab Layout · B Done** (no Size on the Carousel), and while a tile is
held **D-Pad Move · A Drop · X Size · B Drop**.

**Reset** puts the default tiles back: the first press arms it ("Press
again"), a second press within three seconds resets. Choosing Standard or the
Hero grid on the strip shows that layout veiled behind the strip, with
nothing to arrange: the focus stays on the strip until you pick the Carousel
or Widgets, or Done.

**Adding a tile.** **A** on the **Add** tile opens **Add to Home**, a card
over Home with three sections (Widgets, Home cards, Apps and tools) listing
what is not on Home yet, each row with its default size. **Up** / **Down**
move, **L1** / **R1** (PgUp / PgDn) jump between sections, **A** (Enter) adds
the highlighted entry, closes the card and puts the focus on the new tile, and
**B** (Esc) closes the card; the mouse and the wheel work too.

**With a mouse**: click a tile to focus it, its remove badge to remove it and
the focused tile's size tab to resize it. Press on a tile and drag (12 px or
more) to carry it: the tiles make room as it passes over them, and holding it
at the top or bottom edge of the grid scrolls the grid a row at a time. On the
Carousel, drag the middle card sideways: every half card of travel moves it
one place. Click a strip chip to choose it, and the strip's **Done** to
finish. The wheel and a right-click only reach Home when nothing (a menu, a
popover, a panel) is open over it.

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
| **Tab** (keyboard) or **L3** (left stick click) | Switch between scrolling the page and walking its links |
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
| X (keyboard **X** or **C**) | Subtitles on / off (in your subtitle language; CC stays on for the next video) |
| Y (keyboard **Y** or **S**) | Video settings sheet |
| L1 / R1 | Skip −10 / +10 seconds |
| L2 / R2 | Previous / next video in the list that launched this one (search results or the Shorts feed) |
| B | Step back from the settings sheet's Save offline page, close the sheet, then step down: leave full screen, then float the video and go back |

The rewind and fast-forward buttons skip 10 seconds per press. With the settings
sheet open, **Up / Down** move between its rows and **A** applies the row, so
subtitles, full screen, speed and quality are reachable without a touch screen.
A tap on a control also moves the ring there, so touch and D-pad never disagree.

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

The Chat screen has three tabs: the community channel (the JukaHub Discord,
when it is configured), **Juka AI**, and **Saved** for notes that stay on this
device. Every speaker can be opened as a profile card with their picture,
handle, roles and last message.

| Button | Action |
|--------|--------|
| D-pad Up / Down | Walk the history one message at a time (a tall message scrolls first); Down past the newest message reaches the files waiting to be sent, then the composer |
| D-pad Left / Right, L1 / R1 | Previous / next tab; on the waiting files, Left / Right picks one |
| L2 / R2 | Jump to the oldest / newest message |
| Right stick | Scroll the history freely |
| A | Type a message; on a selected message, open its actions; on a waiting file, take it off the message |
| Start | Send what is typed and attached (with nothing to send, Start opens the Start menu as everywhere) |
| X | Attach files: Files opens to pick them |
| Y | The tab's own action: **People** in the community tab, **New chat** (or **Add key**) in Juka AI |
| B / Back | Leave the chat; an open card, list or action bar closes first, and on the waiting files B goes back to the composer |

On a keyboard, Tab / Shift+Tab (or 1–3) pick a tab, Enter types straight into
the composer (Enter sends, Shift+Enter starts a new line, Esc stops typing),
Ctrl+Enter sends, **X** attaches files, Del takes the waiting file under the
cursor off the message, and **R** renames you (so do a click on your name at
the top right and **Rename** on your own card in People, which is there before
you have written anything).

### Sending files

**X** (or the paperclip at the start of the composer) opens Files in pick
mode; its bar says which tab the files are for. **A** on a file attaches it and
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
- **Juka AI** — text files (logs, configs, notes), up to three with a
  question. Juka is shown the file (the start and the end of a long one) with
  the question, a follow-up keeps it, and a file sent alone asks what stands
  out in it.
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
official releases. Whatever JukaHub installed — cores, hakuX, those Windows
emulators — is also kept current without a press by the daily **Emulator
updates** pass (Settings → Games & emulators; Install automatically by
default), which keeps the replaced version for the same roll back.

### Xbox (hakuX on the Smart Pro, xemu on Windows) and Xbox 360 (Windows)

The **Xbox** tile is always on the page. What it offers depends on the device:

| Device | Xbox tile |
|---|---|
| Smart Pro (TG5040) | Installs the experimental [hakuX port](https://github.com/ToastedFrog2/hakux-trimui-smart-pro) |
| Brick Pro | The same install, marked untested: same A133P chip, GPU and sticks, but the port is built for the Smart Pro |
| Brick, Hammer | Not supported: the port is built for the Smart Pro, and Xbox games need two analog sticks the Brick does not have |
| Smart Pro S | Not supported: the port is built for the Smart Pro's A133P and PowerVR GPU; no build exists for the A523 |
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
| A | Start the selected app; its row shows the progress (the first start prepares Windows for a few minutes) |
| Y | Display for this app: Windows (GDI) or OpenGL (handheld) |
| X | The runtime setup page (handheld) |
| Start | Rescan the app folders |
| B | Close setup, then the launcher |

On a keyboard, **Enter** starts, **Y** or **D** switches the display, **X** or
**H** opens setup, **R** or **F5** rescans and **Esc** goes back. Tapping a row
starts it; tapping outside closes the panel.

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
| D-pad Up / Down | Move through PortMaster and installed ports |
| A | Install PortMaster, open it, or launch the selected port |
| B | Back (to Emulators when opened there) |

Tapping a row activates it; tapping outside closes the panel.

## APK Store

Open **Start → Games → APK Store** (not on Windows). It has three tabs: **Your
APKs** (the APKs found on the card, in the Phone Companion's `PhoneDrop` folder
and in the download folders, each with a verdict), **Store** (known titles and
kinds of APK, and how to get them legally) and **Runtime** (AndroidRT, this
device and Waydroid). See [ANDROID.md](ANDROID.md#apk-store).

| Button | Action |
|--------|--------|
| D-pad | Move through the tabs and rows |
| A | On an APK: what you can do with it (Add to Games, Play, Remove from Games); elsewhere: open the row |
| X | Everything the store read about the APK or the catalogue entry |
| Y | Read the folders again |
| L1 / R1 | Previous / next tab |
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
| A | Open a folder or a file; an archive opens its sheet ([Archives](#archives)) and a zipped game in a console folder starts (while Chat is picking files, attach the file, or the marked rows) |
| B / Back | Up one level, or back to Home at the root (while Chat is picking files, back to the chat) |
| Y | The action sheet |
| X | Mark the row under the cursor and step down |
| Mouse / touch | A chip in the breadcrumb bar to jump, the sort chip to change order, a row to open it |

The action sheet (**Y**) holds **New folder**, **Edit text**, **Extract…**, **Send to
chat**, **Rename**, **Copy**, **Cut**, **Paste**, **Delete**, **Select all**,
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

Open it from **Settings → Network → Bluetooth devices**, **Start → Bluetooth**,
search, or the **Controllers** row in PS Remote Play. It needs `bluetoothctl`,
so Windows pairs in its own Settings instead.

| Input | Action |
|-------|--------|
| D-pad / arrows | Move through the rows |
| A / Enter | Switch Bluetooth on or off, pair a device under Nearby, open a paired device's sheet (Connect or Disconnect, Forget), search, or show how to put a pad in pairing mode |
| X / `x` | Search again (ten seconds) |
| L1 / R1, PgUp / PgDn | Page up and down; Home / End jump to the ends |
| B / Esc | Close the sheet, then leave the page |

The page searches once as it opens. Pairing a controller, what its row says and
what to do when a pad shows **No input** are in
[REMOTE_PLAY.md](REMOTE_PLAY.md#bluetooth-controllers).

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
([BUG_REPORTS.md](BUG_REPORTS.md)). **Anonymous diagnostics** is off by default; when on, it sends only
the build, device model, platform, OS/architecture and panic counts — never
designs, filenames, links or account details.

## QR codes

Three codes exist to be scanned by another device: the **Phone Companion**
pairing code, the **Screen Share** link, and **Community & support →
Diagnostics export**. In their cards they are a few hundred pixels square over
a dimmed screen, which is the shape a phone camera reads worst, so each one can
also be enlarged on its own.

| Input | Action |
|-------|--------|
| A (on the selected code), or a tap on it | Fill the screen with the code and raise the backlight to full |
| Up / Down, or Left from Share or Games too (Screen Share) | Select the code; the card opens on **Share** |
| A / B / Esc, or any tap | Close the enlarged code and put the backlight back where it was |

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
| Up / Down | Move between the code, **Share**, **Quality** and **Games too** (handheld only), wrapping |
| Left / Right | On **Quality**: the previous / next preset. On **Share** or **Games too**, Left selects the code and Right comes back |
| A / Enter | Code: enlarge it · Share / Games too: switch · Quality: next preset |
| Y | New link: the old one stops working at once; the Phone Companion's PIN is not changed |
| B / Esc | Close |
| Click / tap | Runs the row (on **Quality** the left half steps down, the right half up); a click outside the card closes it |

On the viewer page, **F**, the full-screen button or a double-click (double-tap)
toggles full screen, and **M** cycles Fit, Fill and Pixel-perfect; the controls
hide after three seconds without the pointer moving. Add `&tv=1` to the link
for a TV browser: no controls, just the picture.

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
firmware's **Menu + volume** (brightness) never opens the orb, and **Menu +
Start** still quits like **Select + Start**. With the on-screen keyboard up,
**Menu** (or **F4**) is voice typing into the field instead, and on the Voice
command page it is that page's own microphone. Over a modal panel, the
first-run tour, the dock, the arcade, JukaLand, a browser game or a running
game, **Menu** keeps its old job (Back). **Settings → Controls → Menu button:
voice** turns the Menu binding off for good; **F4** stays the voice key on a
keyboard.

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
| F4 | The voice orb: talk to JukaHub (hold it to talk; on the Voice command page F4 starts or stops listening) |
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
