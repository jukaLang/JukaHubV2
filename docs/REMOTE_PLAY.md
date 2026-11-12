# PS Remote Play

JukaHub contains its own PS4/PS5 Remote Play client. There is no Python app, no
installer for a third-party port and no language setting: discovery, pairing,
the console list, quality, logs and the launch are all JukaHub's own English UI.
The only external pieces are two native helpers built from chiaki-ng's
`libchiaki`, shipped with JukaHub:

```text
SDCARD/Apps/JukaHub/bin/chiaki-stream   streaming (SDL2 + FFmpeg decode)
SDCARD/Apps/JukaHub/bin/chiaki-regist   PS5 pairing handshake
SDCARD/Apps/JukaHub/libs/brick-stock/   only for a TG5050 SDK build: its FFmpeg and OpenSSL 1.1
```

`chiaki-regist` is only needed for PS5: PS4 pairing is implemented in Go, in
[psremote_ps4.go](../player/psremote_ps4.go), so a PS4 console can be paired even
when the helper is missing.

Nothing has to be copied by hand: a JukaHub installed from a package without
`bin/` (an older one, or a copy of just the binary) downloads both helpers
itself, in the background soon after it starts on Wi-Fi, or when **PS Remote
Play** opens; see [Installing the helpers](#installing-the-helpers).

## Supported consoles

| Console | Pairing | Streaming |
|---|---|---|
| PS4 (firmware 8.0 and newer) | JukaHub, pure Go + PSN Account-ID | `chiaki-stream` |
| PS5 | `chiaki-regist` + PSN Account-ID | `chiaki-stream` |

chiaki's PS4 protocol targets 8, 9 and 10 (firmware older than 7.0, 7.x, and
8.0 or newer) use different registration crypto and endpoints; discovery
reports which generation answered and the pairing follows it. Every PS5, and
every PS4 from firmware 7.0 on, also checks the PSN Account-ID when it pairs
(see [Getting the PSN Account-ID](#getting-the-psn-account-id)).

## Installing the helpers

The helpers come with every TrimUI package of JukaHub (`Apps/JukaHub/bin/`).
When they are not there (a JukaHub installed from a package without them, which
every release before the November 2026 helper build was) JukaHub installs them
itself, once:

1. **In the background.** About a minute after JukaHub starts, and at every look
   after that (every 30 seconds, and at once when you come back from a game),
   the download starts as soon as the handheld is online (a Wi-Fi that wants a
   sign-in is not), its clock is past the date of this build (an older clock
   fails every certificate, so network time goes first), JukaHub is on screen
   (not behind a game, and not on the idle dock or starfield) and nothing else
   is installing them. Saving a PSN Account-ID, on the handheld or
   [from the phone](#getting-the-psn-account-id), is a reason to look at once.
   While it runs, a **Remote Play** chip at the top right of every other screen
   follows the download: tapping it opens PS Remote Play, its **✕** stops the
   download (not once the helpers are being copied into place, when the chip
   has no ✕). Settings → Games & emulators →
   **Download Remote Play automatically** (`remotePlayAutoDownload`, on by
   default) turns the background download off; the panel then installs the
   helpers when it opens.
2. Opening **PS Remote Play** while the handheld is online starts the download
   too, when it has not happened yet. The panel's subtitle and its install row
   say what is happening: **Downloading Remote Play…** with the percentage and
   how many MB have arrived, then **Installing Remote Play…** while the download
   is checked and put in place. You can go on choosing a console or getting the
   Account-ID meanwhile, and closing the panel does not stop it.
3. A PS5 is asked for its PIN only once its pairing helper is in, because the
   PIN Link Device shows lasts five minutes: **A** on it during the download
   says so, its row says *PIN keyboard opens when Remote Play is installed*,
   and the PIN keyboard opens by itself when the install ends (open Link
   Device then). An address typed under **Add console by address** waits the
   same way while an install can come (it may be a PS5), and is asked at once
   otherwise. **Play** on a paired console waits too, and the stream starts by
   itself when the helpers are in, as long as the panel is still on the
   console list with nothing over it. **B** lets go of a waiting Play or PIN
   prompt (the install goes on), and so does closing the panel; both say so,
   and a newer press takes the place of an older one. A PS4 pairs without the
   helpers; only its stream needs `chiaki-stream`.
4. The list then says **Remote Play is installed** for the rest of that visit,
   and a toast says so too (not over the PIN keyboard or the Account-ID card;
   after a background download, on whatever screen is up). JukaHub updates
   bring their own `bin/`, so this happens once.

What it downloads is `JukaHub-RemotePlay-linux-arm64.zip` (`bin/chiaki-stream`,
`bin/chiaki-regist`, `bin/BUILDINFO.txt` and `SHA256SUMS`, a few MB) from the
newest of JukaHub's recent releases on GitHub (the last 100) that has one,
stable releases before betas: a release whose helper build failed is skipped
for an older one. Before anything is installed:

* the archive must match the SHA-256 GitHub publishes for it, and a release
  that carries a signed `release.json` must verify (one whose manifest does not
  list the package is passed over for an older release); the unpacked helpers
  must match the archive's `SHA256SUMS`;
* each helper about to be installed is started once without arguments, as the
  stream's own check does before a hand-off: one this firmware's loader refuses
  (a newer glibc, a missing library) is not installed (the other one still
  is), the panel names the reason, and the refusal is kept in
  `bin/.remote-play-refused`: neither opening the panel nor a press downloads
  that again, only **Retry** does;
* there must be room: the download first, then twice what it unpacks to;
* only a missing helper is written, into `bin/` beside the JukaHub binary,
  under a temporary name renamed into place, `chiaki-stream` last, so an
  interrupted install never looks finished. A helper that is there is never
  replaced.

A dropped connection is resumed: at once, up to twice in one install, when
the download had made progress, and otherwise on the next try (the download
waits in `bin/.remote-play-install/`, which is removed once the helpers are
installed, or when a download fails its check or the firmware refuses it).
With no internet the list offers **Install Remote Play** for when it is back;
after a failure the row says why, and **A** on it (**Retry**) tries again.
In the background, a try that failed because of the network, GitHub's rate
limit or a server error, a clock that was still wrong, or a download that took
too long is repeated after 2, 5, 15 and 30 minutes and then hourly; any other
failure (no recent release has the helpers, a full or read-only card, a
download that did not verify, a certificate the handheld cannot check, or a
stop on the chip) is not repeated by itself until JukaHub starts again, an
Account-ID is saved or the switch is turned back on. Opening the panel still
starts one (two minutes or more after the last try), and **Retry**, **Play**,
a PS5's PIN and **Download now** on the phone start one at once.

## Using it

Open **Start → PS Remote Play**, or press the **PS4** / **PS5** tile in the home
header. The panel lists every console it has paired plus anything the current
scan found.

| Row | A button |
|---|---|
| `Name · PS4/PS5` (paired) | Starts the stream, waking the console first if it is in rest mode ([Waking the console](#waking-the-console)) |
| `Name · PS4/PS5` (not paired) | Asks for the PSN Account-ID once (if none is saved), then for the PIN the console shows |
| Downloading Remote Play… / Installing Remote Play… / Remote Play is not installed / Install Remote Play | Only while a helper is missing: the download's progress (it keeps going if you close the panel), the check and copy that follow it, why it failed (A tries again), or the offer to install ([Installing the helpers](#installing-the-helpers)); *Remote Play is installed* once it is done, and *Remote Play won't run on this firmware* (or *PS5 pairing won't run on this firmware*) when the firmware refused the helpers |
| Scan for consoles | Sweeps the LAN for three seconds |
| Add console by address | Type `192.168.1.x` when discovery cannot see it |
| PSN Account-ID | Opens the Account-ID card: sign in with your phone or browser, or type it |
| Controllers | Which pads the stream gets; A opens the Bluetooth devices page to pair one. While Bluetooth is on it says the stream can stutter ([Bluetooth controllers](#bluetooth-controllers)) |
| Quality | Cycles the stream profile |
| Video codec | Auto, H.264 or H.265 for a PS5 (a PS4 always sends H.264). Auto is H.264 on the Smart Pro, Brick, Brick Pro and Hammer, H.265 on the Smart Pro S |
| Select button | What Select sends in a stream: the touchpad click (default) or Create; Menu + Select sends the other |
| Rumble | Game vibration on the handheld's motor and on connected pads |
| Volume | Left/Right adjusts, 0–100% |
| Last stream | Shown when the last stream did not end well: what happened, and A opens that console's options |
| Close | Leaves the panel |

**X** wakes the selected console without streaming. **Start** (O on a
keyboard) opens the selected console's options. **Y** (or Delete) forgets the
selected console, including its keys. **B** closes the panel; on the
Account-ID card and the console options it goes back to the console list,
while a Play or a PIN prompt waits for the helpers' install it lets go of that
first, and while a wake is waiting it stops the wake first.

### Console options

**Start** on a console's row opens its page:

| Row | What it does |
|---|---|
| Play / Pair | The same as A on the console's row |
| Internet address | The address the console has from outside your home: the router's public address or a dynamic-DNS name (with port forwarding), or a VPN address: an IPv4 address or a host name (IPv6 does not work yet). A port typed with it is left out, since the Remote Play ports are fixed, and JukaHub says so when it was not 9295. JukaHub tries the home address first and uses this one when the console does not answer there. It is kept with the pairing, so the same pairing works at home and away |
| Login passcode | Only if your PlayStation profile asks for a 4-digit passcode when Remote Play starts. Saved here, it is entered for you; without it the stream asks for it on screen |
| Remove the saved passcode | Shown while one is saved |
| Forget this console | Deletes its pairing keys from the handheld |

Both fields are saved only when you press the keyboard's Save. An empty
Internet address field removes the address; an empty passcode field keeps the
saved one. The passcode field is hidden like a password, and the keyboard
forgets what was typed when it closes.

### Pairing a PS5

1. Pick the PS5's row (or **Add console by address**) and press **A**.
2. If no PSN Account-ID is saved yet, the **Account-ID card** opens. Get the
   Account-ID once, as described in
   [Getting the PSN Account-ID](#getting-the-psn-account-id); JukaHub then goes
   straight on to the PIN.
3. On the PS5: **Settings → System → Remote Play → Link Device**. Type the 8
   numbers it shows (spaces are fine) and confirm. Keep Link Device open until
   the row turns into **Play** (an address typed by hand gets its row once it
   is paired; the toast says when).

The PS5 checks the Account-ID against the accounts on the console, so a wrong
one fails before the PIN is even looked at. JukaHub says which stage failed.
On a JukaHub that has no helpers yet, the PIN is asked for once
[their install](#installing-the-helpers) has finished: open Link Device when
the keyboard appears.

### Pairing a PS4

1. Pick the PS4's row and press **A**. On firmware 7.0 or newer the PS4 also
   checks the PSN Account-ID, so the Account-ID card opens first if none is
   saved.
2. On the PS4: **Settings → Remote Play Connection Settings → Add Device**.
   Type the 8 numbers it shows; they are valid for about five minutes.
3. JukaHub runs the registration in Go, stores the keys and the row turns into
   **Play**. A refusal names the console's reason when it gives one: a PIN that
   was wrong or has expired, or an Account-ID the console did not accept.

### Getting the PSN Account-ID

The Account-ID is a number Sony keeps for your PSN account, written as 12
characters of Base64 (for example `AbCdEfGhIjk=`). It is **not** your sign-in
name or Online ID and **not** the PIN, and no console menu shows it, Link
Device included. It is needed once per account; JukaHub keeps it in
`psremote.json`.

The Account-ID card on the handheld offers the first three; the last needs
only your phone:

| Choice | Where | What you do |
|---|---|---|
| **Sign in with your phone** | Handheld (also PC) | Scan the QR code on the card (**A** shows it full screen). The phone opens JukaHub's sign-in page. Tap **Sign in to PlayStation** and sign in with the account you play on the console; the tab ends on a page that says *redirect*. Copy that page's address, paste it into JukaHub's page and tap **Send to handheld**. The handheld fetches the Account-ID and goes on to the PIN by itself. |
| **Sign in in your web browser** | Windows / PC | Sony's page opens in your browser and JukaHub opens the Account-ID field. After signing in, copy the address of the *redirect* page, paste it into the field (Ctrl+V) and press Enter. |
| **Type or paste it** | Anywhere | Type the 12-character Account-ID if you already have it (desktop chiaki-ng shows it after **PSN Login**). The field also takes the long decimal account number other tools print, or a pasted *redirect* address. |
| **The phone's PlayStation card** | Phone Companion page | Any time, without the panel: the page the Home screen's QR code opens has a **PlayStation Remote Play** card. It says whether an Account-ID is saved (shortened, as the panel shows it). Paste the Account-ID, the long account number or a *redirect* address into its field and tap **Save on handheld**, or tap **Sign in to get it** for the sign-in page described above. Under it, the card follows Remote Play's own download on the handheld, with **Download now** (or **Try again**) when it is missing. |

The phone pages are part of the Phone Companion (the server behind the Home
screen's QR code), so the phone has to be on the same Wi-Fi as the handheld,
and the handheld needs the internet to reach Sony. An Account-ID saved from the
phone while PS Remote Play is closed is kept for the next pairing (a toast says
so) and, with **Download Remote Play automatically** on, starts Remote Play's
download when the helpers are missing.

What happens to the sign-in: the *redirect* address carries a one-time code.
JukaHub sends it to Sony's token service over HTTPS — the same exchange
chiaki's `psn-account-id` script makes — reads the account number from Sony's
answer and keeps only the Account-ID worked out from it. Your password never
reaches JukaHub, and the code and the access token are never logged or saved.
The code works once and only for a few minutes: *That sign-in code has expired*
means sign in again and send the new address. Third-party "Account-ID finder"
web pages work too, but they receive your sign-in code; use one only if you
trust it.

## Waking the console

A paired console in rest mode is turned on from the handheld. Press **A** on it
as usual: unless it answered as on in the last 30 seconds, JukaHub first asks
the console how it is, sends the wake signal if it rests (again every five
seconds while it comes up), waits until it reports ready — up to a minute; the
row counts the seconds — gives Remote Play three seconds more and starts the
stream. **X** wakes a console without streaming, for a download or for someone
at the TV. **B** stops waiting; a console that already got the signal may still
turn on.

The console has to keep its network on in rest mode:

| Console | Settings |
|---|---|
| PS5 | Settings → System → Power Saving → Features Available in Rest Mode → **Stay Connected to the Internet** and **Enable Turning On PS5 from Network** |
| PS4 | Settings → Power Save Settings → Set Features Available in Rest Mode → **Stay Connected to the Internet** and **Enable Turning On PS4 from Network** |

Only rest mode can be woken: a console that was shut down completely, or
unplugged, cannot. A wake that fails names these switches.

A paired console's row says what the last scan heard:

| Status | Row says | Meaning |
|---|---|---|
| **Play** | *Ready to stream* | It answered as on (or the first scan is still running) |
| **Wake** | *In rest mode* | A wakes it and starts the stream |
| **Play** | *Not answering on this network* | The last scan did not hear it; A tries to wake it first |
| **Try anyway** | *Did not answer* | Nothing answered at its address for six seconds; A connects without asking, which a console behind a VPN that filters discovery needs |

The wake signal is the WAKEUP datagram chiaki-ng sends: UDP to the console's
own address on its discovery port (9302 for a PS5, 987 for a PS4), with the
pairing's registration key as its credential. It goes only to that address,
never as a broadcast, and only after that address has answered as this console
(its MAC matches the pairing's), so it works on the console's own network and
over a VPN that carries UDP to it, and another device at an old address never
gets it. Away from home, a console with an **Internet address**
(its options) that does not answer at home within 2.5 seconds is asked at the
internet address, and a router that forwards the discovery port to the console
lets it be woken there; if nothing answers there either, the stream goes to the
internet address anyway, as before. The credential can only wake the console:
streaming needs the session key, which never leaves the handheld.

When the router gives the console a new address, the next scan recognises it
by its MAC address and the pairing moves with it, keys included. A console
added by host name keeps its name.

## Controls in a stream

The face buttons are mapped by position, like a DualSense, not by the letters
printed on the TrimUI (which has Nintendo's layout):

| TrimUI | PlayStation |
|---|---|
| Bottom (**B**) / right (**A**) / left (**Y**) / top (**X**) | Cross / Circle / Square / Triangle |
| D-pad | D-pad (or the left stick while *D-pad = left stick* is on) |
| L1, R1 / L2, R2 / sticks | the same (L2 and R2 are analog) |
| L3, R3 (stick clicks; F1, F2 on a Brick) | L3, R3 |
| Start | Options |
| Select | Touchpad click (or Create: the **Select button** row) |
| Menu, tapped (let go within about a second, nothing else pressed) | PS button, sent as Menu is let go |
| Menu + Select | The other of touchpad click and Create |
| Menu + D-pad | A touchpad swipe up, down, left or right |
| Menu + right stick | A finger on the touchpad that follows the stick |
| Menu + R3 | Touchpad click where that finger is |
| Menu + L3 | *D-pad = left stick* on/off (on by default on the Brick and Hammer, which have no sticks) |
| Hold Menu for a second | The stream menu: resume, quit, **Hold PS (Home)**, the Select button, the D-pad mode, rumble, the stream volume, statistics and this table |
| Hold Start + Select (about 1.2 s, both on one pad) | Quit the stream (an on-screen bar fills first). Once both are down, neither reaches the console again until it is let go, so letting go of one early sends nothing |

**Hold PS (Home)** in the stream menu closes the menu and holds the PS button
for about 1.2 seconds: the long press a Menu tap cannot give (the PS5's home
screen, the PS4's quick menu). While the stream menu, the passcode screen or an
error card is up, the console is sent an idle controller, and the button that
closed it does not reach the game.

When the console asks for your PlayStation profile's login passcode, a
passcode saved under the console's options is entered once by itself.
Otherwise four digit boxes appear: Up / Down change a digit, Left / Right move
between them, **A** sends the passcode (Start does not: it is half of the quit
combo) and **B** gives up, also while the screen says *Checking the passcode*.
Giving up ends the stream.

The screen also says what the stream is doing: a card while it connects
instead of a black screen, the controls for six seconds when the picture
starts, a short note when a mode changes or a pad arrives or leaves, battery
warnings at 15, 10 and 5 % while the handheld is not charging (JukaHub's own
warnings cannot show while the stream has the screen), the statistics when
they are on, and a card that says why a stream failed or ended before the
helper exits.

The helper registers the handheld pad's controller mapping itself (the same
X360 mapping JukaHub adds), so the layout above does not depend on what the
firmware's SDL knows. A pad SDL has no mapping for is read as a raw joystick;
one with six axes is read in the X360 order, its triggers included.

A Bluetooth or USB pad connected before or during the stream works alongside
the built-in controls (up to six pads in all), and it keeps its own buttons:
its PS (Guide, Home) button is the console's PS button for as long as you hold
it — the stream menu stays on the handheld's Menu —, its D-pad stays a D-pad
while *D-pad = left stick* is on (that mode is for the handheld's own D-pad),
and on a DualSense or DualShock 4 Create sends Create and the touchpad is the
touchpad: the click and up to two fingers. The console sees one controller
with the buttons of all the pads; each stick direction comes from the pad
pushed furthest that way, the deeper trigger wins, and a pad that disconnects
lets go of everything it was holding. Holding Start + Select (Options + Create)
quits when both are on one pad; Start on one pad and Select on another are just
buttons. Rumble reaches the pad in use for as long as the game asks. Pads with
Nintendo labels (a Switch Pro, an 8BitDo in Switch mode) are read by position,
so Cross is always the bottom button. Create and the touchpad need the
firmware's SDL 2.0.14 or newer.

**DualShock 4 and DualSense over Bluetooth.** When SDL drives the pad itself
(its own PlayStation driver), everything reaches the console: Create, the
touchpad (click and two fingers), rumble, and **motion** — the pad's gyroscope
and accelerometer, plus an orientation the helper works out from them, for
games that aim or steer by tilting. When the pad comes through the kernel's
event device instead, SDL often has no mapping for it (a DualShock 4 on the
TrimUI's 4.9 kernel reports its buttons in the HID report's order); the helper
then maps it itself, by its USB id and its kernel's button order, so Cross,
Circle, the triggers and the PS button land where they should. That way the
touchpad and motion stay on input nodes of their own: a DualSense's Create
button then clicks the touchpad (the stream menu's **Select** row swaps it for
Create), while a DualShock 4 on the older kernel still clicks its own touchpad.
JukaHub's menus map such pads the same way, so a PlayStation pad drives the
launcher too. `psremote.log` says which way each pad came
(`PS4 pad mapped from the kernel's HID report order …`; `motion` and
`touchpad` on the `controller added` line). The handheld's own controls have no
gyroscope; with several motion pads the one used last is the console's.
`psremote.log` lists every pad as it arrives and leaves
(`[native] controller added: …`, marked `built-in` for the handheld's own).
Pairing a Bluetooth pad: [Bluetooth controllers](#bluetooth-controllers).

## Bluetooth controllers

**Settings → Network → Bluetooth devices** pairs a controller with the
handheld. The Bluetooth button in the top bar (left of the Wi-Fi meter), the
Start menu (**Bluetooth**, next to Wi-Fi Networks), Quick Settings (**Bluetooth
Devices**), search (*Pair a controller*, or words like "connect headphones")
and the Remote Play panel's **Controllers** row open the same page.

1. Switch Bluetooth on (the page's first row).
2. Put the pad in pairing mode — DualSense: hold **Create + PS** until the light
   bar blinks quickly; DualShock 4: **Share + PS** until it flashes; Xbox
   Wireless Controller: switch it on, then hold the pair button on top until the
   Xbox button flashes fast; 8BitDo: choose its mode, then hold its pair button.
   **Put a controller in pairing mode** on the page shows the same list.
3. With nothing paired yet the page searches for ten seconds as it opens;
   **Search** (or **X**) runs another. Pick the pad under **Nearby**.
4. JukaHub pairs, trusts and connects it, then waits up to four seconds for the
   system to make a controller of it.

| Paired row | Meaning |
|---|---|
| **Connected** · *works in games and Remote Play* | The system made a controller for the pad |
| **Checking…** | Just connected: the system gets up to four seconds to make a controller of it |
| **No input** · *no controller appeared* | It connected, but the system made no controller for it |
| **Connect** | Paired but not connected: press the pad's PS or Home button, or choose the row and **Connect** |

**A** on a paired device connects or disconnects it; **Y** opens its sheet:
**Connect** or **Disconnect**, and **Forget**. A pad is only called ready when the kernel has made a controller
with its address. **No input**, or a pairing that ends with *did not connect:
this system has no driver for that kind of device*, is what firmware that
supports only Bluetooth LE input (the stock firmware's notes promise "BLE hid")
can do with a classic-Bluetooth pad such as a DualShock 4, a DualSense or many
8BitDo modes. A USB-C cable or a 2.4 GHz USB adapter works instead. Which pads
work on which firmware has not been recorded on hardware yet; the Bluetooth
block in a bug report's log has a `Pad HID` line (HIDP, uhid and Sony's kernel
drivers present or not) that tells.

* A PlayStation pad remembers one system at a time: pairing it with the
  handheld unpairs it from the console until it is plugged into the console
  with a cable.
* On some firmware a pairing does not survive a restart or switching Bluetooth
  off; pair the pad again from the page.
* Bluetooth shares the radio with Wi-Fi on these handhelds, and TrimUI's own
  firmware notes ask to switch it off while streaming. With a Bluetooth pad in
  Remote Play, pick `540p30` if the stream stutters, or use the pad on a cable;
  the panel's **Controllers** row says so while Bluetooth is on.
* The page needs `bluetoothctl` (BlueZ), which the TrimUI firmware has. On
  Windows, pair in **Settings → Bluetooth & devices**; JukaHub uses any
  controller Windows connects.

## Quality

The profiles are the ones the handhelds were tuned with, and the choice is
remembered:

| Profile | Resolution | FPS | Bitrate |
|---|---|---|---|
| `720p30` (default) | 1280×720 | 30 | 8 Mb/s |
| `540p30` | 960×540 | 30 | 4 Mb/s |
| `540p30 eco` | 960×540 | 30 | 3 Mb/s |
| `720p60` | 1280×720 | 60 | 12 Mb/s |
| `1080p30` | 1920×1080 | 30 | 15 Mb/s |

The Smart Pro S decodes 720p30 comfortably. A Brick Pro is happiest at 540p30;
drop to eco when the stream and a download share the Wi-Fi.

## Playing away from home

Remote Play is a direct connection from the handheld to the console. JukaHub
finds consoles on the local network and connects to the address it paired; it
has no PSN relay: it does not do the "remote connection via PSN" (UDP hole
punching through Sony's servers) that chiaki-ng and Sony's own app offer. So
"on the train" works only when the handheld can reach the console's address,
and that takes two things:

1. **Internet for the handheld**: a phone hotspot (tethering) or any Wi-Fi.
2. **A way into the home network**, one of:
   * **a VPN**, for example a Tailscale or ZeroTier subnet router, or a
     WireGuard server, on a PC, NAS or router at home. The handheld (or a
     travel router it connects through) has to be on the same VPN. With a
     subnet route the console keeps its home address (`192.168.1.51`), so the
     console you paired at home works as it is;
   * **port forwarding** on the home router (TCP 9295, UDP 9296–9297; add
     UDP 9302 for a PS5 or 987 for a PS4 to wake the console from rest mode
     while away: [Waking the console](#waking-the-console)), then set the
     console's **Internet address** (Start on its row) to the home's public
     address or a dynamic-DNS name. The pairing made at home is used for it:
     JukaHub streams to the home address when the console answers there, and
     to the internet address when it does not.

Discovery cannot see through either, which does not matter: a paired
console's row is saved, so it is listed without a scan. On mobile data set
**Quality** to `540p30 eco`: 3 Mb/s is about 1.4 GB an hour.

## The screen is handed over, not shared

TrimUI firmware gives one app the display, so the stream cannot run inside
JukaHub's window. Starting a stream writes the session file to `/tmp` (mode
0600), starts a detached hand-off script and quits JukaHub. The script waits
for JukaHub to release SDL and the framebuffer, makes sure no `MainUI` is
drawing and runs `chiaki-stream SESSION`. Holding `START + SELECT`, or **Quit**
in the stream menu, ends the stream.

What happens around the stream depends on the launcher that started JukaHub:

* **Current `launch.sh`** exports `JUKAHUB_SCREEN_HANDOFF=1`. As JukaHub quits
  it writes the hand-off helper's pid to `.screen-handoff` beside the binary.
  `launch.sh` waits for that pid with `MainUI` and the firmware supervisors
  still stopped, so nothing draws over the stream, and starts JukaHub again
  when the stream ends: you come back to JukaHub, not to the stock menu.
* **An older `launch.sh`** (no `JUKAHUB_SCREEN_HANDOFF`): the script stops the
  firmware UI itself and, when the stream ends, resumes the supervisors and
  hands the screen back to the stock menu. It starts `MainUI` only if none has
  come back after ten seconds, so two menus never share the framebuffer. Open
  JukaHub from the stock menu afterwards.

## Files

These paths use `JukaHub` for the app folder name. If the folder was renamed,
use its installed name instead (for example, `Apps/JukaHub-Trimui/psremote.log`).

| Path | Contents |
|---|---|
| `Apps/JukaHub/data/psremote.json` | Paired consoles, quality, volume, Account-ID (0600) |
| `Apps/JukaHub/psremote.log` | Helper output for the last streams |
| `Apps/JukaHub/bin/` | `chiaki-stream` and `chiaki-regist`: from the JukaHub package, or installed by the panel, which also writes `BUILDINFO.txt` when it installs both |
| `Apps/JukaHub/bin/.remote-play-refused` | The release whose helpers this firmware refused, and why; Retry on the panel's row clears it |
| `Apps/JukaHub/bin/.remote-play-install/` | A helper download under way, or one cut off and waiting to be resumed (removed once the helpers are installed or a download fails its check) |
| `Apps/JukaHub/data/psremote-last.txt` | How the last stream ended, written by the helper; JukaHub reads and removes it on its next start |
| `/tmp/jukahub-ps-*.conf` | Session file; deleted by the helper after reading |

Deleting `psremote.json` unpairs every console; the consoles can be paired
again. Discovery never writes credentials: an unpaired console is only a row.

Forgetting a console deletes its `regist_key`, `rp_key` and MAC from the file.
The console itself still lists JukaHub under **Link Device → previously
connected**, so remove it there too if you want a clean slate.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `Remote Play is not installed` · `no recent JukaHub release has them · try after an update` | None of the last 100 releases carries `JukaHub-RemotePlay-linux-arm64.zip` (its CI build failed or has not run): update JukaHub when the next release is out |
| `… no connection to GitHub` / `… GitHub is limiting downloads` / `… the download was cut off` | The helpers download from GitHub: connect to Wi-Fi, or wait a few minutes, then **A** on the row; a cut-off download resumes |
| `… the clock is wrong · set the date and time, then try again` | The certificate check needs a sane date: set the date and time, then **A** on the row |
| `… can't verify GitHub · copy JukaHub's package from a PC` | This firmware has no certificate store to check GitHub with: copy `JukaHub-Trimui.zip` from a release whose helper build succeeded (its `bin/` holds the helpers) onto the card from a PC |
| `… the download did not verify · try again` | The archive or a helper in it did not match its checksum; if it keeps happening, wait for the next release |
| `Remote Play won't run on this firmware` (or `PS5 pairing won't run on this firmware`) · `chiaki-stream: needs GLIBC_… · firmware has GLIBC_…` | The downloaded helper named there needs something this firmware lacks; it was not installed (a helper that runs was), and only **Retry** downloads again |
| `… SD card full · needs …, has … · free some space` | The install needs the download plus twice what it unpacks to: free some space, then **A** on the row |
| `Could not pair — Remote Play is not installed yet: …` | The pairing needed `chiaki-regist`, which did not install; the rest names why, and the list's Remote Play row has **Retry** |
| `Pairing failed — chiaki-regist won't run here: …` | The `chiaki-regist` in `bin/` was built for another firmware (the line names what it needs): delete it, and on Wi-Fi the panel installs the release's, which it starts once first, so a refusal is said, not installed |
| No consoles found | Different network, or Remote Play is off on the console — add the address by hand |
| `Pairing failed at network: …` | Console unreachable; check the address and that Link Device is open |
| `Pairing failed at protocol: …` | Wrong/expired PIN, or a PS5 Account-ID that does not own Remote Play |
| `… reason 80108b02 — the console did not accept the PSN Account-ID …` | The Account-ID belongs to another account: sign in again with the account you play on |
| `That sign-in code has expired` | The *redirect* address works once and for a few minutes: sign in again |
| `This device cannot check Sony's certificate` | The firmware has no certificate bundle JukaHub can use: type the Account-ID, or sign in on a PC |
| `Sony's certificate does not fit this device's date` | The handheld's clock is far off: set the date and time, then sign in again |
| Stream immediately returns | Read `psremote.log` in the installed JukaHub folder; the helper prints the reason, and JukaHub shows it as a toast and as the panel's **Last stream** row |
| `Remote Play could not reach …` | The console is off (and not in rest mode with network wake on), Remote Play is off on it, or it is on another network; away from home, check the port forwarding or the VPN |
| `… asks for its 4-digit login passcode` | Your PlayStation profile has a passcode: type it on the stream's screen, or save it under the console's options |
| `Remote Play could not show the video …` | The decoder could not keep up: the toast names the other **Video codec** to try (H.264 after H.265, H.265 after H.264 on a PS5) or a lower **Quality**; a PS4 only sends H.264 |
| `The login passcode saved for … was refused` | The passcode was changed on the console: change or remove it under the console's options (Start on its row) |
| `Remote Play stopped unexpectedly (code N)` | The helper crashed or was stopped; `psremote.log` has its last lines |
| `… did not answer — press A to connect anyway. To wake it, turn on Enable Turning On PS5 from Network …` | The console is shut down (not in rest mode), on another network, or its rest-mode network switches are off ([Waking the console](#waking-the-console)); A again connects anyway |
| `… did not come out of rest mode within a minute` | **Enable Turning On PS5/PS4 from Network** is off, or the console is busy, for example installing an update |
| `… is not at 192.168.1.51 any more — looking for it on this network` | Something else answers at the console's old address: the router gave the console a new one. JukaHub scans, and the pairing moves to the new address when the console answers there. The wake signal is never sent to a device that is not this console |
| `Could not start Remote Play — the helper won't run here: …` | The helper was built for a newer firmware (its glibc) or misses a library; the rest of the line names it. JukaHub stays open instead of quitting into a stream that cannot start; delete `bin/chiaki-stream`, and on Wi-Fi the panel installs the release's, which it starts once first |
| A Bluetooth pad shows **No input**, or *did not connect: this system has no driver for that kind of device* | The firmware cannot use that pad over Bluetooth ([Bluetooth controllers](#bluetooth-controllers)); use a USB cable or a 2.4 GHz adapter |

The helpers are built from chiaki-ng's library sources for aarch64 by the
`remote-play-helpers` workflow, packed into every TrimUI archive and attached
on their own as `JukaHub-RemotePlay-linux-arm64.zip` to each release whose
helper build succeeded; see
[BUILDING.md](BUILDING.md) and [player/native](../player/native/README.md).
Windows builds still compile the panel and PS4 pairing, but `chiaki-stream` is
Linux ARM64 only, so the tiles report Remote Play as unavailable there.
