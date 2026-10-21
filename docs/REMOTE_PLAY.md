# PS Remote Play (Chiaki-ng)

JukaHub can install and launch the **trimui-chiaki-ng** port of Chiaki from
the Start menu. The port is a Python front end with native aarch64 helpers and
the SDL2, FFmpeg and OpenSSL libraries used by the Brick Pro Stock OS.

## Supported consoles

| Build | PS4 | PS5 |
|-------|-----|-----|
| Stable release (`v0.3.x`) | Yes, LAN PIN pairing | No |
| `v0.4.0-beta2` (**PS5 beta**, opt-in) | Yes | Beta pairing + streaming, never confirmed on real hardware |

The port's *published* releases are PS4-only: the author shipped one PS5 build
(`v0.4.0-beta2`, "in-app PS5 Account-ID pairing flow"), then withdrew it — the
release and its ZIP now return 404 — and `docs/PS5_STATUS.md` says PS5
development stopped. Its README states plainly: `PS5 không được hỗ trợ` ("PS5 is
not supported") and "the old beta tags are kept for technical reference only".

JukaHub can still install that build, because the **git tag is intact** and
carries the complete application plus a `manifest.json` with a SHA-256 for every
file. It is installed as a *separate* app, so the stable PS4 install and its
pairing are never touched:

```text
SDCARD/Apps/Chiaki/       stable, PS4
SDCARD/Apps/Chiaki-PS5/   opt-in PS5 beta
```

Treat it as an experiment owned by the port's author, not a supported path. If
it does not pair, the stable build is exactly where you left it.

## Launch from the home screen

The home header has a **PS5** tile immediately left of the Phone Companion QR
tile (it never covers the code). One press does everything:

| Tile state | Press |
|---|---|
| `PS5 PLAY` (beta installed) | Launches the PS5 build |
| `PS5 GET` (not installed) | Downloads the PS5 beta, then launches it automatically |
| `PS5 WAIT` (an install is running) | Only reports that the installer is busy |

The tile is part of the D-pad focus order: from the card directly beneath it
(**Media** when the header shows network, weather, clock and battery, as on the
Smart Pro; **Files** when the status cluster is short) press **Up** to select
`PS5`, then **A** to press it. The tile lifts, gains a bright ring and adds
"A" to its caption while selected, and dips while pressed, so the handheld can
reach it without a touchscreen. `Up` again from the tile reaches the clock, and
`Down` returns to the card it sits over. The tile is hidden on viewports too
narrow to hold it beside the QR without covering the greeting, and then its
focus entry is disabled rather than left pointing at something invisible;
**Start → PS Remote Play** reaches the same actions.

### The screen is handed over, not shared

TrimUI firmware gives one app the display: JukaHub's own launcher stops the
stock `MainUI` before JukaHub starts. A second SDL app therefore cannot come up
behind a running JukaHub, which is why the old in-process launch (hide the
window, then run the port) produced no picture.

Launching now writes a small handoff script to `/tmp` and quits JukaHub, so SDL
and the framebuffer are released. The script waits for JukaHub to exit, lets its
launcher bring the stock menu back, then takes the screen from it the same way
that launcher takes it from `MainUI`: pause `runtrimui.sh`, stop `MainUI`, and
run the port's own `launch.sh`. When the stream ends it resumes the firmware
supervisors and restores the stock menu. Expect:

* JukaHub to close and the screen to go black for a moment,
* Chiaki-ng to start by itself,
* the stock menu to return when you exit the stream (**START + SELECT**),
* **Start → JukaHub** to reopen JukaHub.

If the port cannot start, the reason is written to `Apps/Chiaki/Chiaki-loi.txt`
and `Apps/Chiaki/Chiaki-jukahub.log`; the stock menu still comes back.

## Install

1. Connect the handheld to the same Wi-Fi as the console.
2. Open **Start → PS Remote Play**.
3. Select **Install Chiaki-ng**.
4. JukaHub downloads the release ZIP from
   `github.com/nlkcodenew/trimui-chiaki-ng`, verifies the SHA-256 digest
   published by GitHub, and installs it at:

   ```text
   SDCARD/Apps/Chiaki/
   ```

The package contains its own runtime libraries:

```text
Apps/Chiaki/libs/brick-stock/
├── libSDL2-2.0.so.0
├── libavcodec.so.60
├── libavutil.so.58
├── libswresample.so.4
├── libswscale.so.7
├── libopus.so.0
├── libcrypto.so.1.1
├── libssl.so.1.1
└── ...
```

The Smart Pro S launcher uses the firmware's SDL2/FFmpeg instead. Python 3.8+
is required; TrimUI firmware 1.0.4 or newer includes it. JukaHub looks for the
interpreter in the same places as the port's own launcher (PATH, the app's
`python/bin`, `SDCARD/System/bin` and PortMaster's `exlibs`), and the panel
reports a missing Python or SDL2 while still letting you try the launch.

## Interface language

Chiaki-ng ships its own Vietnamese and English catalogues and stores the choice
as `"language": "VI"` or `"EN"` in `Apps/Chiaki/settings.json`. The PS5 beta
keeps a separate file in `Apps/Chiaki-PS5/settings.json`.

**Start → PS Remote Play → Interface language** (and **PS5 interface
language** when the beta is installed) toggles EN/VI and keeps every other
setting and pairing key untouched. A fresh install from JukaHub starts in
English even though every release ZIP ships `settings.json` with
`"language": "VI"`: the installer rewrites that one key on a first install and
never touches a language the user already chose.

The catalogue is not the whole UI. Five files carry text written before it
existed, so JukaHub also rewrites those literals in place, in whichever language
the settings select, before the port starts:

| File | Text |
|------|------|
| `rh/i18n.py` | adds the missing `pair_required` key to both catalogues |
| `rh/screens/home.py` | the "not paired" toast, which used to print the raw key |
| `rh/chiaki.py` | stream start and failure messages shown on the home screen |
| `rh/screens/pair.py` | the reason appended to `Pair failed: %s` |
| `rh/ps5_regist.py` | the PS5 Account-ID and helper errors shown while pairing |

Each rewrite is an exact full-string match in a known file: protocol code,
settings and pairing keys are never touched, a release that words a line
differently is left alone, and the file is replaced through a temporary file so
a card pulled mid-write cannot truncate a Python module. Switching back to
Vietnamese restores the original wording of those messages; the two catalogue
fixes stay in both languages, because a missing key and a broken toast are bugs
rather than translation. The patch runs again before every launch, so a port
update that replaces these files is translated automatically.

Some diagnostics printed by the port's launcher stay Vietnamese, and so do the
GitHub issue bodies the port's reporter builds; only the menus and the messages
listed above are translated.

## Why JukaHub does not embed its own client

Chiaki-ng is a complete Remote Play implementation: console discovery,
registration crypto, H.264/HEVC decode, audio, input and latency control, with
a desktop graphics stack. Reimplementing it inside JukaHub would mean replacing
all of that protocol work rather than just a UI, and this handheld has neither
Vulkan nor the memory for the upstream Qt client. The TrimUI port exists
because it already did that work for this hardware; JukaHub's job is to
install, verify, launch and configure it.

The same reasoning rules out replacing the port's menu with a JukaHub-drawn
SDL2 screen: the menu is only the shell around host discovery, the pairing
protocol and the native stream helper, so a replacement would still have to
drive all three. Rewriting the handful of untranslated strings is the smaller,
reversible change, and it keeps the port's own screens — which the author
updates — intact.

## Install the PS5 beta

1. Open **Start → PS Remote Play**.
2. Select **Install PS5 beta** (about 12 MB).
3. JukaHub downloads the `v0.4.0-beta2` source tree from GitHub, unpacks it in a
   staging folder, checks every file against the SHA-256 values in the tag's
   `manifest.json`, and only then swaps the app into `Apps/Chiaki-PS5/`.
4. Launch it with **Launch PS5 beta**. Its settings and pairing live in its own
   folder and are preserved when you reinstall.

## Pair a PS5 (beta)

The PS5 path needs two different numbers; neither is the other.

| Value | What it is | Where it comes from |
|-------|------------|---------------------|
| PSN Account-ID | 12-character Base64 of your PSN `user_id` (8 bytes) | The **PSN Login** button in the official desktop chiaki-ng |
| Remote Play PIN | 8 digits | The PS5's own **Link Device** screen |

Steps:

1. Keep the PS5 and the handheld on the same LAN. Enable **Settings → System →
   Remote Play → Enable Remote Play** and leave the console awake for the first
   attempt.
2. On the PS5 open **Settings → System → Remote Play → Link Device**. It shows
   an 8-digit PIN.
3. Run **Launch PS5 beta**, scan, and pick the host the app detects as a PS5.
   It asks for **Step 1/2**, the 12-character Base64 PSN Account-ID. It is *not*
   your PSN Online ID, and it is not the 8-digit PIN. Get it from the desktop
   chiaki-ng's PSN Login and type the resulting string on the handheld.
4. **Step 2/2** is the 8-digit PIN from the console. The helper registers
   directly against the console's IP.
5. For a first attempt, turn off the console's 4-digit login code (that is a
   different code again) and use 720p/30, SDR, no HDR.

The PS5 build answers failures with `PS5-*` codes and writes the detail to
`Apps/Chiaki-PS5/Chiaki-loi.txt`. JukaHub cannot fix PS5 pairing: the port's
author stopped that work, so a failure is reported, not repaired.

## Pair a PS4

1. On the PS4, enable **Settings → Remote Play Connection Settings → Enable
   Remote Play**.
2. Turn the console on and leave it at the home screen. The stable build does
   not wake a PS4 from Rest Mode.
3. In Chiaki-ng, select **Scan PS4**, choose the console, and press **Y**.
4. On the PS4, choose **Add Device** and enter the 8-digit PIN shown by
   Chiaki-ng.
5. Select the paired console and press **A** to stream.

Pairing keys live in `Apps/Chiaki/paired_hosts.json`. JukaHub never deletes
that file during an update; it also preserves `settings.json`, `chiaki.conf`
and the app's logs.

## Controls and quality

D-pad, ABXY, shoulders, triggers, sticks, Start and Select are mapped by the
port. Hold **Start + Select** for about 1.2 seconds to return to the app.

Recommended starting points:

| Device | Profile |
|--------|---------|
| Smart Pro S | 720p30 / 8000 kbps |
| Brick Pro | 540p30 / 3000 kbps, or 720p30 / 8000 kbps on a strong network |

Network quality matters more than the device. Lower the resolution or bitrate
in the port's settings if the stream stutters.

## Logs

| File | Contents |
|------|----------|
| `Apps/Chiaki/Chiaki-loi.txt` | Port and native-helper output |
| `Apps/Chiaki-PS5/Chiaki-loi.txt` | PS5 beta port and helper output (`PS5-*` codes) |
| `Apps/Chiaki/Chiaki-jukahub.log` | Handoff script and port launcher output from JukaHub launches |
| `Apps/JukaHub/logs.txt` | JukaHub's own log |

## Removing

Use **Start → PS Remote Play → Open install folder** to find the app, or delete
`Apps/Chiaki` from the SD card. Deleting it removes the paired-console keys;
pair the PS4 again after reinstalling.
