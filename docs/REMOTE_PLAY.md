# PS Remote Play (Chiaki-ng)

JukaHub can install and launch the **trimui-chiaki-ng** port of Chiaki from
the Start menu. The port is a Python front end with native aarch64 helpers and
the SDL2, FFmpeg and OpenSSL libraries used by the Brick Pro Stock OS.

## Supported consoles

The current TrimUI port targets **PS4 Remote Play**. Its README says
`PS5 không được hỗ trợ` ("PS5 is not supported"), and the project's
`docs/PS5_STATUS.md` says PS5 pairing, PSN login and H.265 streaming are not
being developed further.

JukaHub therefore labels the feature **PS4 only** in the installer. Do not
expect the PS5 to appear as a pair-able console in this build.

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
is required; TrimUI firmware 1.0.4 or newer includes it. The panel reports a
missing Python or SDL2 instead of starting a client that cannot render.

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
| `Apps/Chiaki/Chiaki-jukahub.log` | JukaHub's launch wrapper output |
| `Apps/JukaHub/logs.txt` | JukaHub's own log |

## Removing

Use **Start → PS Remote Play → Open install folder** to find the app, or delete
`Apps/Chiaki` from the SD card. Deleting it removes the paired-console keys;
pair the PS4 again after reinstalling.
