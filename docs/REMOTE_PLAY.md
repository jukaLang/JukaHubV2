# PS Remote Play

JukaHub contains its own PS4/PS5 Remote Play client. There is no Python app, no
installer for a third-party port and no language setting: discovery, pairing,
the console list, quality, logs and the launch are all JukaHub's own English UI.
The only external pieces are two native helpers built from chiaki-ng's
`libchiaki`, shipped with JukaHub:

```text
SDCARD/Apps/JukaHub/bin/chiaki-stream   streaming (SDL2 + FFmpeg decode)
SDCARD/Apps/JukaHub/bin/chiaki-regist   PS5 pairing handshake
SDCARD/Apps/JukaHub/libs/brick-stock/   Brick Pro runtime (FFmpeg, OpenSSL 1.1)
```

`chiaki-regist` is only needed for PS5: PS4 pairing is implemented in Go, in
[psremote_ps4.go](../player/psremote_ps4.go), so a PS4 console can be paired even
when the helper is missing.

## Supported consoles

| Console | Pairing | Streaming |
|---|---|---|
| PS4 (firmware 8.0 and newer) | JukaHub, pure Go | `chiaki-stream` |
| PS5 | `chiaki-regist` + PSN Account-ID | `chiaki-stream` |

PS4 firmware 8, 9 and 10 use different registration crypto and endpoints;
discovery reports which generation answered and the pairing follows it.

## Using it

Open **Start → PS Remote Play**, or press the **PS4** / **PS5** tile in the home
header. The panel lists every console it has paired plus anything the current
scan found.

| Row | A button |
|---|---|
| `Name · PS4/PS5` (paired) | Starts the stream |
| `Name · PS4/PS5` (not paired) | Asks for the PIN shown under **Link Device** |
| Scan for consoles | Sweeps the LAN for three seconds |
| Add console by address | Type `192.168.1.x` when discovery cannot see it |
| PSN Account-ID | Edit the account id a PS5 needs |
| Quality | Cycles the stream profile |
| Volume | Left/Right adjusts, 0–100% |
| Close | Leaves the panel |

**Y** (or Delete) forgets the selected console, including its keys.

### Pairing a PS4

1. On the console: **Settings → System → Remote Play → Link Device**.
2. Pick the console row, type the 8-digit PIN.
3. JukaHub runs the registration, stores the keys and the row turns into
   **Play**.

### Pairing a PS5

1. On the PS5: **Settings → System → Remote Play → Link Device**.
2. Set the **PSN Account-ID** first if it is empty. This is the 12-character
   Base64 of your PSN `user_id` (8 bytes), which the official desktop chiaki-ng
   shows after **PSN Login**.
3. Pick the console row and type the 8-digit PIN.

The PS5 checks the Account-ID against the account that owns the Remote Play
entitlement, so a wrong one fails before the PIN is even looked at. JukaHub says
which stage failed.

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

## The screen is handed over, not shared

TrimUI firmware gives one app the display, so the stream cannot run inside
JukaHub's window. Starting a stream writes the session file to `/tmp` (mode
0600), starts a detached handoff script and quits JukaHub:

* the script waits for JukaHub to release SDL and the framebuffer,
* stops the firmware UI the same way JukaHub's launcher does,
* runs `chiaki-stream SESSION`,
* resumes the firmware supervisors and restores the stock menu when the stream
  ends, starting `MainUI` itself if nothing else did.

`START + SELECT` ends the stream. Reopen **Start → JukaHub** afterwards.

## Files

| Path | Contents |
|---|---|
| `Apps/JukaHub/psremote.json` | Paired consoles, quality, volume, Account-ID (0600) |
| `Apps/JukaHub/psremote.log` | Helper output for the last streams |
| `/tmp/jukahub-ps-*.conf` | Session file; deleted by the helper after reading |

Deleting `psremote.json` unpairs every console; the consoles can be paired
again. Discovery never writes credentials: an unpaired console is only a row.

Forgetting a console deletes its `regist_key`, `rp_key` and MAC from the file.
The console itself still lists JukaHub under **Link Device → previously
connected**, so remove it there too if you want a clean slate.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `Helper missing — missing …/chiaki-stream` | The release did not include `bin/`; reinstall JukaHub |
| No consoles found | Different network, or Remote Play is off on the console — add the address by hand |
| `Pairing failed at network: …` | Console unreachable; check the address and that Link Device is open |
| `Pairing failed at protocol: …` | Wrong/expired PIN, or a PS5 Account-ID that does not own Remote Play |
| Stream immediately returns | Read `Apps/JukaHub/psremote.log`; the helper prints the reason |

The helpers are built from chiaki-ng's library sources for aarch64; see
[BUILDING.md](BUILDING.md) and the `native/` folder in the chiaki-ng checkout.
Windows builds still compile the panel and PS4 pairing, but `chiaki-stream` is
Linux ARM64 only, so the tiles report Remote Play as unavailable there.
