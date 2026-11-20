# Controller Mode

Controller Mode turns the handheld into a game controller for a PC or a phone,
and gives it back to JukaHub with one tap of **Menu**.

Open it from **Tools → Controller Mode**, the **Apps** screen, the **Start
menu**, **Home search** (try "controller", "gamepad", "dsu" or "jukapad") or the
**Controller Mode** button in **Quick Settings**.

## Pick a link

The page lists the ways the handheld can connect. Choose one and press **A**.

| Link | The other side sees | Needs on the other side |
|---|---|---|
| **USB cable to a computer** | an Xbox 360 controller (Windows, Steam), or a generic gamepad (macOS, Linux, Android) | nothing; a data USB-C cable |
| **Wi-Fi to a PC** | an Xbox 360 (or DualShock 4) controller | the small JukaPad receiver (Windows or Linux) |
| **DSU for PC emulators** | a DSU controller in Dolphin, Cemu and the yuzu forks | nothing; type the address into the emulator |
| **Bluetooth gamepad (beta)** | a standard Bluetooth LE gamepad (Android, Linux; Windows as DirectInput) | nothing; pair from Bluetooth settings |

If the community **GamePad to PC** app or a **Bluetooth pad** app (for example
"Xbox Pad BT") is installed in the card's Apps folder, the page lists it under
**Installed apps** too. Those run on their own; JukaHub comes back when they
close.

## Playing

When the other side connects, the page switches to a live picture of the
controller: every button, trigger and stick lights up exactly as the PC or phone
receives it.

- **Tap Menu** to come back to JukaHub. The PC sees every button released; the
  link stays up, so **A** on the page resumes and **B** stops it.
- **Hold Menu** (about half a second) to send the controller's **Guide / Xbox /
  Home** button. Menu pressed together with another button is Guide plus that
  button (Steam's Guide shortcuts), never a tap.
- On a keyboard, **Esc** takes you back.
- Leaving the page stops the link.

By default the screen turns off 15 seconds after the link starts, to save
battery; tapping Menu lights it again. Set **Screen while playing** to **Stays
on** to keep the live controller visible.

## Layouts

The **Layout** row chooses how the handheld's controls become the controller
(Left/Right cycles, **A** edits):

- **Face buttons**: by **position** (the bottom button is A, what PC games
  expect) or by **label** (the button printed A sends A, Nintendo-style).
- **D-pad**: a D-pad, or the **left stick** (for games that ignore the D-pad).
- **Stick dead zone** (0–30 %), **invert the right stick's Y**, and whether
  **holding Menu sends Guide**.

Three layouts are built in — Standard, Nintendo labels and Retro (D-pad as
stick). The first change to a built-in one saves a copy of your own. Each PC or
phone remembers the layout it used last.

## USB cable to a computer

Choose the **shape** on the page before starting: **Xbox 360 controller**
(Windows' own driver and Steam take it; XInput games work) or **Generic gamepad**
(macOS, Linux, Android and DirectInput games, no driver). Then plug the handheld
into the computer with a data cable. USB file transfer pauses while the cable is
a controller and comes back afterwards; the USB-C port must not be in host
(OTG) mode. The port's own diagnostics (what the kernel offers) are on Tools →
System → Controller mode — see [USB gamepad](USB_GAMEPAD.md).

## Wi-Fi to a PC

The PC runs the small **JukaPad receiver**; the handheld serves it, so no
website is needed.

**Get the receiver.** Start **Wi-Fi to a PC**. The row **Get the PC receiver**
shows an address like `http://192.168.1.42:8324`. Open it in the PC's browser
(same Wi-Fi) and download:

- **Windows**: `jukapad-receiver.bat` and `jukapad_receiver.py` into the same
  folder, then double-click the .bat. It needs Python 3.8+ (python.org; tick
  "Add python.exe to PATH"). The first run installs `vgamepad`, which installs
  the ViGEmBus driver — accept its installer. When Windows Firewall asks, allow
  Python on **private** networks.
- **Linux**: `python3 jukapad_receiver.py`. It creates an Xbox 360-style pad
  through `/dev/uinput`; if that is not allowed it prints the fix (a udev rule
  and the `input` group).
- **macOS** is not supported (no virtual controllers); use the USB cable as a
  generic gamepad, or DSU for emulators.

**Pair once.** The PC appears on the handheld's page as "Found — A pairs".
Press A: both screens show the same 6-digit code. If they match, choose **Codes
match** on the handheld and press **Enter** in the receiver window (type `n` to
refuse). From then on the handheld connects by itself whenever the receiver runs
on that PC. **Forget <PC>** removes it; on the PC, delete
`~/.jukapad/paired.json` (Windows: `%USERPROFILE%\.jukapad\paired.json`) to
forget every handheld.

Pairing uses an X25519 key exchange with a commit-then-reveal code, so a
listener on the network learns nothing and a device in the middle is caught by
the codes not matching. Every packet after that is authenticated and numbered;
the PC releases every button within a second if the link drops. Rumble from
games vibrates the handheld.

**Steam / XInput.** On Windows the pad is an Xbox 360 controller (XInput).
`jukapad-receiver.bat --pad ds4` makes it a DualShock 4 instead. If Steam Input
also wraps it and inputs double, turn Steam Input off for that game.

**Receiver options.** `--name "Living room PC"`, `--port 8320`,
`--tsp <handheld IP>` (for guest networks and routers with AP/client isolation
that block discovery), `--pad xbox|ds4`, `--selftest` (checks the protocol
offline).

**Ports.** Receiver UDP 8320; handheld UDP 8323 and the download page on TCP
8324 (a free port is used if one is taken). Nearby devices keeps 8321/8322.

## DSU for PC emulators

Dolphin, Cemu and the yuzu forks (Eden, Citron, …) can read the handheld with
nothing installed. Start **DSU for PC emulators**; the page shows the address
(`<handheld IP>:26760`).

- **Dolphin**: Controllers → Alternate Input Sources → enable DSU Client, add
  the address; then pick the `DSUClient/0/…` device in the controller's
  configuration and map.
- **Cemu**: Input settings → API **DSUController** → Settings: the handheld's IP,
  port 26760 → Controller 1.
- **Eden / Citron / other yuzu forks**: Controls → Advanced → enable UDP
  controllers, add the server, pick the UDP device.
- **Citra / Azahar / Ryujinx** read only motion over DSU, which the handheld
  does not have — use Wi-Fi to a PC there.

DSU has no password: anyone on the same network can read the pad while it runs,
so stop it when you are done. The page shows "Connected" while an emulator reads
it.

## Bluetooth gamepad (beta)

1. Choose **Bluetooth gamepad (beta)**. The handheld's Bluetooth is renamed
   **JukaHub Pad** while it runs.
2. On the phone or PC, open Bluetooth settings → *Pair new device* / *Add
   device* → **JukaHub Pad**.
3. When the handheld shows a six-digit code, check it matches and press **A**.
   A pairing without a code still asks on the handheld first.

Next time, start it again: a phone or PC paired before reconnects on its own.
**Remove pairing with …** on the page forgets one (forget "JukaHub Pad" on that
device too).

- **Android** and **Linux**: a standard gamepad.
- **Windows**: a "HID-compliant game controller" (DirectInput). XInput-only
  games need Steam Input (add the game to Steam) or a wrapper such as x360ce —
  or use the USB cable or Wi-Fi, which are Xbox 360 controllers.
- **iPhone and iPad** do not accept generic Bluetooth gamepads.
- **macOS**: untested.

It needs the firmware's BlueZ to host GATT services and LE advertising; if it
cannot, the page says so.

## Troubleshooting

- *The PC is never found (Wi-Fi)*: both must be on the same network, not a guest
  network. With AP/client isolation start the receiver with
  `--tsp <handheld IP>`, and allow UDP 8320 inbound in the PC's firewall.
- *"Lost <PC>"*: weak Wi-Fi or the receiver closed; it reconnects by itself.
- *"It forgot this handheld"*: the PC's pairing file was removed — press A on it
  to pair again.
- *USB: nothing happens on the computer*: use a data cable (not charge-only),
  and check Tools → System → Controller mode for what the kernel offers.
- *A game ignores the D-pad*: choose the Retro layout (D-pad as stick).
- *A and B are swapped*: switch the layout's face buttons between position and
  label.
