# VNC and Telnet servers

Two servers in **Tools → Network tools → Servers & sharing**, next to FTP and
SSH / SFTP. Both are off until you start them, answer only devices on your own network, and switch
themselves off when nobody has been connected for 15 minutes, when the
handheld joins another network, when the startup lock comes up, and when
JukaHub quits.

## VNC — see and control the screen

Press **A** on **VNC screen & control** to start it. The sheet shows the
address and the password. Open any VNC app on a computer, tablet or phone on
the same network — RealVNC Viewer, TigerVNC, macOS Screen Sharing (Finder →
Go → Connect to Server → `vnc://address`), bVNC or AVNC on Android — enter the
address and the password, and it shows the handheld's screen and controls it.

- **What it shows:** JukaHub, and on the handheld the game on screen too (the
  panel's framebuffer, the source Screen Share reads). About 12 frames a
  second on the handheld's own screens (15 on a computer) and up to 10 in a
  game, and only the 64 × 64 squares that changed are sent, compressed (ZRLE)
  when the app supports it. Pictures are taken only while a viewer is
  connected.
- **Keys** work as on a USB keyboard: arrows are the D-pad, Enter is A, Esc
  is B, X and Y are X and Y, PgUp/PgDn page (L1/R1), Home/End jump (L2/R2),
  Tab switches tabs, F10 opens the Start menu, and typing goes into text
  fields.
- **In a game** the keys press the handheld's own buttons: arrows, Enter = A,
  Esc or Backspace = B, X, Y, PgUp/PgDn = L1/R1, Home/End or Q/E = L2/R2,
  Tab = Select, F10 = Start, F1 = Menu (the in-game menu).
- **The mouse** moves JukaHub's pointer while it is up (mouse mode,
  Select + L1, or the right stick's pointer): the left button clicks, the
  wheel scrolls. Otherwise it is ignored.
- **Clipboard:** text you copy in the VNC app lands on JukaHub's clipboard
  (up to 64 KB).
- **Two viewers** at most; a third is told so.

The password has 8 characters (VNC's limit) and is kept until **New
password** replaces it, so the app can remember it. A new password
disconnects everyone signed in with the old one. A wrong password costs two
seconds, and five wrong ones in a minute lock that device out for a minute.

The port is 5900 (display 0), so the address alone is enough. With another
port (`vnc_port`) the sheet shows `address::port`, the form VNC apps read as a
port.

**Windows:** the server runs on the desktop build too, for JukaHub's own
screens. A game there plays in a window of its own, which JukaHub can neither
show nor type into.

**Security.** VNC's sign-in uses DES, which is weak by today's standards, and
the picture and keys travel unencrypted: anyone who has the password controls
the handheld. Use it at home. The server takes only private (192.168.x.x,
10.x.x.x, 172.16–31.x.x), link-local and loopback addresses, and Tailscale's
100.64.0.0/10 — through Tailscale the connection is encrypted end to end.

## Telnet — a shell from a computer

Press **A** on **Telnet** to start it, then on the computer type the command
the sheet shows, e.g. `telnet 192.168.1.20` (PuTTY: connection type Telnet).
Enter the password from the sheet and you get the same shell as the
Terminal's interactive sessions (bash where it is installed, else sh; cmd.exe
on Windows), starting in the SD card. The window size follows the client's.

- Port 23 on the handheld, 2323 on a computer (`telnet_port`).
- The password is new every time the server starts and is never saved or
  logged. Three wrong tries end the connection, each after a two-second wait;
  five in a minute lock the address out for a minute.
- Two sessions at most. Closing the window ends the shell and everything it
  started.

**Telnet is not encrypted.** Everything — the password, every command and
its output — crosses the network as plain text, and the shell runs with
JukaHub's rights (root on the handheld). Use it only on a network you trust,
and stop it when you are done. SSH is the safe choice: for files, JukaHub's
SSH / SFTP server is encrypted.

## Settings

| Key | Default | Meaning |
|---|---|---|
| `vnc_port` | `5900` | VNC port (also `JUKAHUB_VNC_PORT`) |
| `telnet_port` | `23` handheld, `2323` computer | Telnet port (also `JUKAHUB_TELNET_PORT`) |
| `vncPassword` | made on first use | the VNC password; a secret, left out of config exports |

## Not yet checked on hardware (VNC-HW-1)

- that a game's picture reaches `/dev/fb0` on each model (the Smart Pro S
  draws through DRM, as Screen Share notes);
- that keys written into the firmware's pad reach RetroArch and ports;
- RealVNC Viewer, TigerVNC and macOS Screen Sharing against the server.
