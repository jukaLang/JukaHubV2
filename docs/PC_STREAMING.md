# PC streaming (Moonlight, Vibepollo, Apollo, Sunshine)

JukaHub has its own Moonlight client. It streams games and the desktop from a
Windows or Linux PC running [Vibepollo](https://github.com/Nonary/Vibepollo),
[Apollo](https://github.com/ClassicOldSong/Apollo),
[Sunshine](https://github.com/LizardByte/Sunshine) or GeForce Experience to
the handheld, over home Wi-Fi or from away.

Open it from the **PC** pill at the end of the Remote Play group in the Home
header (after PS4, PS5 and X1), from Start → Games → **PC streaming
(Moonlight)**, from the Games tab, or by searching for "moonlight",
"vibepollo" or "sunshine". The pill's dot is lit once a PC is paired.

## Why not PortMaster's Moonlight

The PortMaster port is the desktop Moonlight (Qt) running on a 1 GB handheld:
its interface draws with the GPU the video needs, it decodes in software
without being told how to split the work, its controls are a generic mapping,
and pairing happens inside a mouse-and-keyboard UI. JukaHub keeps the same
split it uses for PS Remote Play instead:

- **JukaHub** (Go) finds the PC, pairs with it, lists its games with their box
  art, and launches or resumes one, with JukaHub's own screens and controls.
- **`moonlight-stream`** (a small native helper in `Apps/JukaHub/bin`, built
  from the current [moonlight-common-c](https://github.com/moonlight-stream/moonlight-common-c),
  the protocol core every Moonlight client shares) takes the screen for the
  stream: FFmpeg decodes H.264 on all four cores (the PC is asked to cut each
  frame into four slices for that), SDL shows it, Opus sound is kept under
  80 ms, and the pads are sent to the PC as an Xbox controller.

### Nonary's Moonlight

Nonary, who makes Vibepollo, also maintains a Moonlight PC fork
([Nonary/moonlight-qt](https://github.com/Nonary/moonlight-qt), the "VRR
Moonlight Client"). Its headline features are VRR frame pacing, the PyroWave
codec (a wired-LAN codec decoded on the GPU) and DualSense haptics. None of
them fits a TrimUI: the panels run at a fixed 60 Hz, the handhelds have Wi-Fi
only, and there is no Vulkan to decode PyroWave. Standard H.264/HEVC is what
Vibepollo offers every other client, and that is what JukaHub uses. Vibepollo's
own guide also says to start with standard Moonlight.

On a **PC**, JukaHub does not stream itself: Play opens Moonlight PC with
`Moonlight stream <PC> <game>`. Install Nonary's build there if you want VRR
and PyroWave (its installer replaces stock Moonlight; the Flatpak uses the same
`com.moonlight_stream.Moonlight` id). Moonlight PC needs that PC paired in
Moonlight PC itself once.

## What works well with Vibepollo and Apollo

- **The handheld's own screen size.** JukaHub asks for exactly the panel's
  resolution (1280×720 on a Smart Pro, 1024×768 on a Brick) and, with
  **Virtual display for this handheld** on (the default), sends the
  `virtualDisplay=1` request Artemis sends. Vibepollo and Apollo then stream a
  virtual screen made at exactly that size, so the Brick gets a 4:3 desktop
  with no black bars and nothing is scaled on the handheld. Plain Sunshine
  ignores the request and streams its own screen.
- **A named client.** Pairing tells the PC "JukaHub" and the device name, so
  Vibepollo's and Apollo's client list shows which handheld is which and keeps
  its permissions apart.
- **Your Playnite library.** Vibepollo's Playnite apps come with their covers,
  and the games page shows them as a grid; a game already running on the PC
  has a **Running** badge and Play resumes it.
- **Up-to-date protocol.** The helper is built from current moonlight-common-c
  (the RTSP handshake is encrypted when the host offers it, `corever=1`).

## Setting up

1. Install Vibepollo (or Apollo / Sunshine) on the PC and open its web page
   (`https://<the PC>:47990`).
2. On the handheld, open **PC streaming**. PCs on the same network show up by
   themselves (mDNS); otherwise choose **Add a PC by address** and type its
   address (a host name, a VPN address or an internet address works too;
   `host:port` for a host on another base port).
3. Choose the PC. JukaHub shows a 4-digit PIN and a QR code. On the PC's web
   page open the **PIN** tab, type the PIN and a name for the handheld (or
   scan the QR with your phone, which opens that page). The handheld finishes
   by itself.
4. Pick a game and press A. On the handheld JukaHub hands the screen to the
   stream and comes back when it ends.

Apollo and Vibepollo give the first paired client full permissions; a second
device may only be allowed to view streams until you grant it more in the
web page's client list.

## Check this handheld (Smart Pro, Brick)

The PC list has a **Check this handheld** row (on the handheld only). It needs
no PC: JukaHub steps aside for about 10-30 seconds while `moonlight-stream`
tests the things a stream depends on, on the device itself, and comes back
with one message:

- **Picture:** opens the screen the way a stream does and decodes a built-in
  720p test clip made like a Sunshine stream (H.264 High, four slices a frame,
  about 9 Mbps), timing every frame with the stream's own decoder settings;
  the test picture (a zooming fractal) shows while it runs.
- **Sound:** plays a short chime through the stream's sound path.
- **Controls and rumble:** lists the pads it sees (the built-in "TRIMUI
  Player1" among them), buzzes the motor, and asks you to press A.
- **CPU:** notes the CPU's governor and the highest clock it reached.

The measured time per frame (scaled to the panel's size) picks the quality
for every PC whose Quality you never changed: **Auto** shows as "Auto:
Balanced" when 60 fps at full size keeps up (under 11 ms a frame), "Auto:
Light" when 3/4 size does, else "Auto: Steady 30". A Quality you choose
yourself always wins. The result is kept in `moonlight/device-check.json`;
run the check again after a firmware or JukaHub update. Auto assumes H.264;
with H.265 the handheld decodes on one core, so pick a lighter preset.

While a stream (or the check) runs, JukaHub sets the CPU governor to
`performance` and switches Wi-Fi power saving off (`iw`, else `iwconfig`),
and puts both back when it ends; `moonlight.log` lists each change.

## Settings (Y on a PC)

| Setting | Choices |
|---|---|
| Quality | **Auto** (default: the handheld check's pick, Balanced until a check has run), **Balanced** (panel size, 60 fps, 10 Mbps), **Steady 30** (panel size, 30 fps, 8 Mbps), **Light** (3/4 of the panel size, 60 fps, 6 Mbps), **Sharp** (panel size, 60 fps, 15 Mbps), **Custom** (width, height, fps, bitrate) |
| Video codec | **H.264** (default: easiest for the handheld to decode, on four cores), H.265 (sharper per bit, one core), Auto (the PC picks) |
| Virtual display for this handheld | On (default) / off |
| Play sound on the PC too | Off (default) / on |
| Quit the game when I leave | Off (default) / on: the game is closed on the PC after every stream |
| Rumble, Stream volume | |
| Address | the typed address, tried before the LAN address; blank clears it |
| Unpair / Forget this PC | removes it here; also remove JukaHub from the PC's client list (Sunshine-family hosts have no unpair request) |

When a stream struggles, JukaHub says so the next time it opens: the helper
records the average decode time, and a handheld that could not keep up at
60 fps is pointed at Light or Steady 30.

## Controls in a stream

The PC sees one Xbox controller; buttons map by position, so the bottom face
button is A whatever is printed on it.

| TrimUI | PC |
|---|---|
| Face buttons (bottom / right / left / top) | A / B / X / Y |
| D-pad, L1, R1, L2, R2, L3, R3, sticks | the same |
| Start / Select | Start / Back |
| Menu (tap) | Xbox Guide |
| Menu + L3 | D-pad moves the left stick (stickless Brick) |
| hold Menu | stream menu: Resume, Mouse mode, Stream stats, Rumble, Volume, D-pad mode, Send to the PC (Alt+Tab or Win), Controls, Disconnect, Quit the game on the PC |
| hold Start + Select | disconnect (the game keeps running on the PC) |

**Mouse mode** (stream menu) drives the PC's desktop: left stick = pointer,
right stick = scroll, bottom / right button = left / right click, D-pad =
arrow keys, Start / Select = Enter / Esc.

Bluetooth and USB pads work at the same time and are merged into the one
controller, as in PS Remote Play.

## Waking the PC and playing away from home

**X** on a PC sends Wake-on-LAN once JukaHub has seen the PC switched on (that
is when the PC reports its network card's address); the PC's network card must
have Wake-on-LAN enabled. Away from home, put the PC's VPN or internet address
in **Address**: JukaHub tries it first, and a stream to an address outside the
handheld's own network encrypts video as well and uses smaller packets.

## Troubleshooting

- **"Could not connect"** names the ports that may be blocked. Vibepollo and
  Sunshine need TCP 47984, 47989, 47990 (web page), 48010 and UDP 47998-48000
  through the PC's firewall.
- **"No video arrived"**: UDP 47998-48000 are blocked, or a VPN drops them.
- **"Refused the stream"**: the PC answered but would not start the game; the
  log on Vibepollo's / Sunshine's web page says why.
- **Choppy picture**: try Light or Steady 30, keep H.264, and switch Bluetooth
  off when nothing is connected (it shares the radio with Wi-Fi).
- **"The streamer is not installed"**: the release's Remote Play package
  carries `moonlight-stream`; JukaHub offers to download it, the same way it
  gets the PS Remote Play helpers.

## Files

| File | What |
|---|---|
| `moonlight-hosts.json` (JukaHub's data folder) | the PCs, their pinned certificates and settings |
| `moonlight/` (data folder) | this handheld's client key, certificate and id; deleting it means pairing again |
| `moonlight-art/` (data folder) | cached covers, refreshed daily |
| `moonlight.log` (beside `psremote.log`) | the helper's log for the last streams |

The helper's session and status files, its build and its controls in detail
are in [player/native/README.md](../player/native/README.md#moonlight-helper-moonlight-stream).
